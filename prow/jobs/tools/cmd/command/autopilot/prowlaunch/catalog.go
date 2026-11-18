// Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//     http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

// Package prowlaunch is the ACK Autopilot Dispatcher's real launcher (Plan 7).
// It builds ProwJobs itself, for an allowlist of workflows, the way
// agent-plugin's pkg/prowjob does (golden-tested against it), creates them in
// the prow namespace with the pod's own ServiceAccount, reads each finished
// run's agent-result.json from the Prow logs bucket, and aborts runs. It opens
// no network listener.
package prowlaunch

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Files the Dispatcher reads from the test-infra checkout it runs in.
const (
	// AgentWorkflowsFile is agent-plugin's workflow file; add-field comes
	// from it, as it does for agent-plugin.
	AgentWorkflowsFile = "prow/agent-workflows/agent-workflows.yaml"
	// JobsImagesFile names the Prow job images; sdk-bump runs in its
	// auto-generate-controllers image, like the Plan 6 periodic.
	JobsImagesFile = "prow/jobs/images_config.yaml"
	// SDKBumpImageKey is the images_config.yaml key of the sdk-bump image.
	SDKBumpImageKey = "auto-generate-controllers"
	// maxFileBytes bounds every file read here.
	maxFileBytes = 1 << 20
)

// Allowed is the allowlist: the only workflows the Dispatcher can start, and
// the Prow job name each runs as. add-field's and triage's names are
// agent-plugin's ("agent-workflow-<workflow>"). sdk-bump is defined in this package, not in
// agent-workflows.yaml, where any /agent comment could start it with ack-bot's
// token (Plan 6 decision 12).
var Allowed = map[string]string{
	"add-field": "agent-workflow-add-field",
	"sdk-bump":  "autopilot-sdk-bump",
	// Plan 8: tool-less, holds no GitHub token, writes only its
	// agent-result.json. agent-plugin's name for the workflow.
	"triage": "agent-workflow-triage",
	// Plan 9: the new-resource lane, agent-plugin's add-resource job.
	"add-resource": "agent-workflow-add-resource",
	// Plan 9: ack-dod on a PR head, defined in this package (DOD): no
	// secrets, so the authoring pod cannot forge its verdict.
	"dod": "autopilot-dod",
}

// Workflow is one workflow the Dispatcher may start. The yaml fields are the
// ones agent-workflows.yaml has (agent-plugin pkg/prowjob Workflow); the rest
// shape the pod of a workflow defined in code.
type Workflow struct {
	Description            string                  `yaml:"description"`
	Image                  string                  `yaml:"image"`
	Command                []string                `yaml:"command"`
	RequiredArgs           []string                `yaml:"required_args"`
	OptionalArgs           []string                `yaml:"optional_args"`
	Timeout                string                  `yaml:"timeout"`
	Environment            map[string]string       `yaml:"environment"`
	EnvironmentFromSecrets map[string]SecretKeyRef `yaml:"environmentFromSecrets"`
	Resources              *Resources              `yaml:"resources"`
	ExtraRefs              []ExtraRef              `yaml:"extra_refs"`
	E2E                    bool                    `yaml:"e2e"`

	pod podShape
}

type SecretKeyRef struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key"`
}

type Resources struct {
	CPU    string `yaml:"cpu"`
	Memory string `yaml:"memory"`
}

type ExtraRef struct {
	Org       string `yaml:"org"`
	Repo      string `yaml:"repo"`
	BaseRef   string `yaml:"base_ref"`
	PathAlias string `yaml:"path_alias"`
	Env       string `yaml:"env"`
	WorkDir   bool   `yaml:"-"`
}

// podShape is what differs between agent-plugin's pod and a code-defined one.
type podShape struct {
	Cluster, ServiceAccount string
	// The Secrets Store CSI volume: its name, SecretProviderClass and mount.
	SecretsVolume, SecretProviderClass, SecretsMount string
	// ArgEnv passes args as environment variables (arg -> variable) instead
	// of --flags. An arg with no entry is checked but not passed.
	ArgEnv map[string]string
	// GracePeriod is the decoration grace period; zero means agent-plugin's
	// 15 minutes.
	GracePeriod time.Duration
	// DoNotDisrupt annotates the job (and so its pod) with
	// karpenter.sh/do-not-disrupt: "true".
	DoNotDisrupt bool
	// CensorSecret, when set, is a Kubernetes secret mounted read-only at
	// /etc/github-censor only so that Prow's sidecar censors its value in the
	// logs (the sidecar censors secret volumes, not a CSI mount).
	CensorSecret string
	// ControllerRef adds <org>/<service>-controller at main as an extra ref
	// (the dod job checks the PR out of it).
	ControllerRef bool
	// NoServiceAccountToken sets automountServiceAccountToken: false: the
	// pod gets no Kubernetes API token (the dod job; Plan 9 review F14).
	NoServiceAccountToken bool
	// SigningSecret, when set, is a Kubernetes secret mounted read-only at
	// SigningMount with mode 0400 (root only): the key that signs the dod
	// verdict, which the sandboxed build uid cannot read (Plan 9 review).
	SigningSecret, SigningMount string
}

// agentPod is agent-plugin's pod (pkg/prowjob CreateWorkflowProwJob).
var agentPod = podShape{
	Cluster: "build", ServiceAccount: "workflow-runner",
	SecretsVolume: "agent-secrets", SecretProviderClass: "agent-secrets", SecretsMount: "/mnt/secrets-store",
}

// Vars resolve the ${TOKEN}s in agent-workflows.yaml and images_config.yaml.
// The agent-workflow-config Helm chart resolves the same two for agent-plugin.
type Vars struct {
	ImagesRepo   string // PROW_IMAGES_REPO_URI
	TestInfraOrg string // TEST_INFRA_ORG
}

var (
	imagesRepoRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?(/[a-z0-9]([a-z0-9._-]*[a-z0-9])?)+$`)
	orgRE        = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	tokenRE      = regexp.MustCompile(`\$\{[A-Z_]+\}`)
	tagRE        = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
)

func (v Vars) validate() error {
	if !imagesRepoRE.MatchString(v.ImagesRepo) {
		return fmt.Errorf("PROW_IMAGES_REPO_URI %q is not an image repository", v.ImagesRepo)
	}
	if !orgRE.MatchString(v.TestInfraOrg) {
		return fmt.Errorf("TEST_INFRA_ORG %q is not a GitHub org", v.TestInfraOrg)
	}
	return nil
}

// resolve replaces the two tokens and fails on any other, as the Helm chart
// does: a literal ${...} would otherwise reach a pod spec.
func (v Vars) resolve(b []byte) ([]byte, error) {
	if err := v.validate(); err != nil {
		return nil, err
	}
	s := strings.ReplaceAll(string(b), "${PROW_IMAGES_REPO_URI}", v.ImagesRepo)
	s = strings.ReplaceAll(s, "${TEST_INFRA_ORG}", v.TestInfraOrg)
	if m := tokenRE.FindString(s); m != "" {
		return nil, fmt.Errorf("unresolved %s", m)
	}
	return []byte(s), nil
}

func readFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxFileBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxFileBytes)
	}
	return b, nil
}

// decodeStrict decodes one YAML document and rejects unknown keys, so a new
// agent-workflows.yaml key that shapes the pod cannot be silently dropped.
func decodeStrict(path string, b []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// LoadAgentWorkflows reads agent-workflows.yaml with its tokens resolved.
func LoadAgentWorkflows(path string, v Vars) (map[string]Workflow, error) {
	b, err := readFile(path)
	if err != nil {
		return nil, err
	}
	if b, err = v.resolve(b); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var doc struct {
		Workflows map[string]Workflow `yaml:"workflows"`
	}
	if err := decodeStrict(path, b, &doc); err != nil {
		return nil, err
	}
	for name, w := range doc.Workflows {
		w.pod = agentPod
		doc.Workflows[name] = w
	}
	return doc.Workflows, nil
}

// LoadImage reads one image reference from an images_config.yaml.
func LoadImage(path, key string, v Vars) (string, error) {
	b, err := readFile(path)
	if err != nil {
		return "", err
	}
	if b, err = v.resolve(b); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	var doc struct {
		ImageRepo string            `yaml:"image_repo"`
		Images    map[string]string `yaml:"images"`
	}
	if err := decodeStrict(path, b, &doc); err != nil {
		return "", err
	}
	tag, ok := doc.Images[key]
	if !ok || !tagRE.MatchString(tag) {
		return "", fmt.Errorf("%s: no valid image %q", path, key)
	}
	return doc.ImageRepo + ":" + tag, nil
}

// SDKBump is the sdk-bump workflow (Plan 6): the pod of the
// autopilot-sdk-bump-<service> periodic (prow/jobs/templates/periodics/
// autopilot-sdk-bump.tpl) with preset-github-secrets inlined, since presets
// apply only to jobs Prow loads from its config. Its args reach
// scripts/autopilot-sdk-bump.sh as environment variables. The real launcher
// runs it for real (AUTOPILOT_SDK_BUMP_WRITE=true): the launcher being on is
// the switch.
func SDKBump(image, org string) Workflow {
	return Workflow{
		Description:  "ACK Autopilot sdk-bump (Plan 6), started by the Dispatcher",
		Image:        image,
		Command:      []string{"/bin/bash", "-c", "./scripts/autopilot-sdk-bump.sh"},
		RequiredArgs: []string{"service", "sdk-to", "work-item", "change-class"},
		OptionalArgs: []string{"mode"},
		Timeout:      "3h",
		Environment: map[string]string{
			"AUTOPILOT_SDK_BUMP_WRITE": "true",
			"AUTOPILOT_ORG":            org,
			// preset-github-secrets (prow/config/templates/config-ConfigMap.yaml).
			"GITHUB_ACTOR":    "ack-bot",
			"GITHUB_EMAIL_ID": "82905295",
			"TEST_INFRA_ORG":  org,
		},
		EnvironmentFromSecrets: map[string]SecretKeyRef{"GITHUB_TOKEN": {Name: "prowjob-github-pat-token", Key: "token"}},
		Resources:              &Resources{CPU: "8", Memory: "8Gi"},
		ExtraRefs: []ExtraRef{{
			Org: org, Repo: "test-infra", BaseRef: "main",
			PathAlias: "github.com/aws-controllers-k8s/test-infra", WorkDir: true,
		}},
		pod: podShape{
			Cluster: "default", ServiceAccount: "periodic-service-account",
			SecretsVolume: "github-token", SecretProviderClass: "prow-secrets", SecretsMount: "/etc/github",
			ArgEnv: map[string]string{"service": "SERVICE", "sdk-to": "SDK_TO", "work-item": "WORK_ITEM", "mode": "AUTOPILOT_SDK_BUMP_MODE"},
			// As the Plan 6 periodic (autopilot-sdk-bump.tpl): a 3h run that
			// Karpenter must not consolidate away, and the PAT censored.
			GracePeriod: 10 * time.Minute, DoNotDisrupt: true, CensorSecret: "prowjob-github-pat-token",
		},
	}
}

// The dod job's verdict signing key (Plan 9 review): the secret, its volume
// and its mount. The secret's data key is "key".
const (
	DodSigningSecret = "autopilot-dod-signing-key"
	DodSigningVolume = "autopilot-dod-signing"
	DodSigningMount  = "/etc/autopilot-dod-signing"
)

// DOD is the Dispatcher's gate job (Plan 9): scripts/autopilot-dod.sh in
// dispatched mode on one PR head, in the auto-generate-controllers image like
// the <service>-autopilot-dod presubmit. It holds no secret but its verdict
// signing key (root-only, 0400) and no AWS identity, runs as the presubmits'
// ServiceAccount, and writes verdict.json, verdict.sig and gate-verdict.txt. The script fetches pull/<pr>/head into the
// controller checkout and refuses any other head than head-sha.
func DOD(image, org string) Workflow {
	return Workflow{
		Description:  "ACK Autopilot ack-dod gate (Plan 9), started by the Dispatcher",
		Image:        image,
		Command:      []string{"/bin/bash", "-c", "./scripts/autopilot-dod.sh"},
		RequiredArgs: []string{"service", "pr", "head-sha", "base-sha", "pr-author", "work-item", "change-class"},
		Timeout:      "2h",
		// The org of the controller repo and of the verdict's subject.
		Environment: map[string]string{"TEST_INFRA_ORG": org},
		Resources:   &Resources{CPU: "4", Memory: "8Gi"},
		ExtraRefs: []ExtraRef{
			{Org: org, Repo: "code-generator", BaseRef: "main", PathAlias: "github.com/aws-controllers-k8s/code-generator"},
			{Org: org, Repo: "runtime", BaseRef: "main", PathAlias: "github.com/aws-controllers-k8s/runtime"},
			{Org: org, Repo: "test-infra", BaseRef: "main", PathAlias: "github.com/aws-controllers-k8s/test-infra", WorkDir: true},
		},
		pod: podShape{
			Cluster: "default", ServiceAccount: "pre-submit-service-account",
			ArgEnv: map[string]string{
				"service": "SERVICE", "pr": "AUTOPILOT_DOD_PR", "head-sha": "AUTOPILOT_DOD_HEAD_SHA",
				"base-sha": "AUTOPILOT_DOD_BASE_SHA", "pr-author": "AUTOPILOT_DOD_PR_AUTHOR",
				"work-item": "AUTOPILOT_DOD_WORK_ITEM", "change-class": "AUTOPILOT_DOD_CLASS",
			},
			GracePeriod: 10 * time.Minute, DoNotDisrupt: true, ControllerRef: true,
			NoServiceAccountToken: true,
			SigningSecret:         DodSigningSecret, SigningMount: DodSigningMount,
		},
	}
}

// Catalog holds the allowlisted workflows, ready to build jobs from.
type Catalog struct {
	workflows map[string]Workflow
}

// NewCatalog takes add-field (required), triage and add-resource (optional)
// from agentWorkflows and adds sdkBump and dod. Every other workflow in
// agentWorkflows is ignored: it is not on the allowlist.
func NewCatalog(agentWorkflows map[string]Workflow, sdkBump, dod Workflow) (*Catalog, error) {
	af, ok := agentWorkflows["add-field"]
	if !ok {
		return nil, errors.New("agent-workflows.yaml has no add-field workflow")
	}
	c := &Catalog{workflows: map[string]Workflow{"add-field": af, "sdk-bump": sdkBump, "dod": dod}}
	// Plan 9: add-resource has add-field's shape (agent-plugin's pod, e2e,
	// the agent token); the admission policy pins it to that shape.
	if ar, ok := agentWorkflows["add-resource"]; ok {
		c.workflows["add-resource"] = ar
	}
	if len(dod.EnvironmentFromSecrets) > 0 || dod.E2E {
		return nil, errors.New("workflow dod must have no environmentFromSecrets or e2e")
	}
	// Plan 8: triage comes from agent-workflows.yaml like add-field, when the
	// file has it. It must hold no secret, run no e2e and clone nothing:
	// Build then mounts no secrets volume, and the admission policy denies
	// any other triage pod.
	if tr, ok := agentWorkflows["triage"]; ok {
		if len(tr.EnvironmentFromSecrets) > 0 || tr.E2E || len(tr.ExtraRefs) > 0 {
			return nil, errors.New("workflow triage must have no environmentFromSecrets, e2e or extra_refs")
		}
		c.workflows["triage"] = tr
	}
	for name, w := range c.workflows {
		if err := w.validate(); err != nil {
			return nil, fmt.Errorf("workflow %s: %w", name, err)
		}
	}
	return c, nil
}

func (w Workflow) validate() error {
	switch {
	case w.Image == "":
		return errors.New("image is empty")
	case len(w.Command) == 0:
		return errors.New("command is empty")
	case w.pod.ServiceAccount == "":
		return errors.New("no pod shape")
	}
	if _, err := w.timeout(); err != nil {
		return err
	}
	return nil
}

func (w Workflow) timeout() (time.Duration, error) {
	d, err := time.ParseDuration(w.Timeout)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("timeout %q is not a positive duration", w.Timeout)
	}
	return d, nil
}

// FromCheckout builds the catalog from a test-infra checkout.
func FromCheckout(root string, v Vars) (*Catalog, error) {
	aw, err := LoadAgentWorkflows(filepath.Join(root, AgentWorkflowsFile), v)
	if err != nil {
		return nil, err
	}
	image, err := LoadImage(filepath.Join(root, JobsImagesFile), SDKBumpImageKey, v)
	if err != nil {
		return nil, err
	}
	return NewCatalog(aw, SDKBump(image, v.TestInfraOrg), DOD(image, v.TestInfraOrg))
}
