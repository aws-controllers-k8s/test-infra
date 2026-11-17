package prowlaunch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dispatcher"
)

// Labels and annotations the Dispatcher puts on its jobs. The ack-autopilot/
// keys are the only ones agent-plugin's jobs do not have.
const (
	LabelManagedBy  = "ack-autopilot/managed-by"
	ManagedBy       = "autopilot-dispatcher"
	LabelKeyHash    = "ack-autopilot/key-hash"
	LabelGeneration = "ack-autopilot/generation"
	LabelRun        = "ack-autopilot/run"
	LabelService    = "ack-autopilot/service"
	// LabelWorkflow is agent-plugin's workflow-type label.
	LabelWorkflow = "workflow-type"
	// AnnotationRequest is the SHA-256 of the launch request the job was
	// built from; Launch adopts an existing job only if it matches.
	AnnotationRequest  = "ack-autopilot/request"
	AnnotationWorkItem = "ack-autopilot/work-item"
)

// Namespace is where Prow reads ProwJobs (prow/config/values.yaml
// prowJobNamespace).
const Namespace = "prow"

// utilityImages are agent-plugin's (pkg/prowjob generator.go); the golden
// test keeps them equal.
var utilityImages = UtilityImages{
	CloneRefs:  "public.ecr.aws/eks-distro-build-tooling/prow-clonerefs:v20260316-26fa34da6",
	InitUpload: "public.ecr.aws/eks-distro-build-tooling/prow-initupload:v20260316-26fa34da6",
	Entrypoint: "public.ecr.aws/eks-distro-build-tooling/prow-entrypoint:v20260316-26fa34da6",
	Sidecar:    "public.ecr.aws/eks-distro-build-tooling/prow-sidecar:v20260316-26fa34da6",
}

// launchIDRE is a dispatcher launch id: a run (r<n>) or an sdk-bump refresh
// (f<8 hex>) of one item generation. It is the job's name.
var launchIDRE = regexp.MustCompile(`^autopilot-([0-9a-f]{16})-g([0-9]{1,4})-(r[1-9][0-9]{0,2}|f[0-9a-f]{8})$`)

var (
	bucketURLRE = regexp.MustCompile(`^s3://[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
	quantityRE  = regexp.MustCompile(`^[0-9]{1,6}(m|Mi|Gi)?$`)
)

// Config is what every job the Dispatcher builds shares.
type Config struct {
	// Bucket is the Prow logs bucket, "s3://<name>": decoration
	// gcs_configuration.bucket, where the Dispatcher reads results.
	Bucket string
	// Org owns the controller repos (TEST_INFRA_ORG).
	Org string
	// Now stamps status.startTime.
	Now func() time.Time
}

func (c Config) validate() error {
	if !bucketURLRE.MatchString(c.Bucket) {
		return fmt.Errorf("logs bucket %q is not s3://<bucket>", c.Bucket)
	}
	if !orgRE.MatchString(c.Org) {
		return fmt.Errorf("org %q is not a GitHub org", c.Org)
	}
	if c.Now == nil {
		return fmt.Errorf("no clock")
	}
	return nil
}

// Build turns a launch request into the ProwJob to create. It refuses any
// workflow not in Allowed, any launch id that is not a dispatcher launch id,
// and any arg the workflow does not declare or whose value has the wrong
// shape. For add-field the job equals what agent-plugin builds for
// "/agent add-field <args>" on <org>/<service>-controller (issue 0, author
// ack-autopilot), apart from the name, startTime and the ack-autopilot/
// labels and annotations.
func (c *Catalog) Build(cfg Config, req dispatcher.LaunchRequest) (*ProwJob, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	w, ok := c.workflows[req.Workflow]
	job, allowed := Allowed[req.Workflow]
	if !ok || !allowed {
		return nil, fmt.Errorf("workflow %q is not on the allowlist", req.Workflow)
	}
	m := launchIDRE.FindStringSubmatch(req.LaunchID)
	if m == nil {
		return nil, fmt.Errorf("launch id %q is not a dispatcher launch id", req.LaunchID)
	}
	hash, gen, run := m[1], m[2], m[3]
	if strings.HasPrefix(run, "f") && (req.Workflow != "sdk-bump" || req.Args["mode"] != ModeRefresh) {
		return nil, fmt.Errorf("launch id %q names a refresh, which only sdk-bump --mode refresh is", req.LaunchID)
	}
	if err := checkArgs(req.Workflow, w, req.Args, "sha256:"+hash); err != nil {
		return nil, fmt.Errorf("%s: %w", req.Workflow, err)
	}
	timeout, err := w.timeout()
	if err != nil {
		return nil, err
	}
	if req.Timeout > 0 && req.Timeout < timeout {
		timeout = req.Timeout
	}
	keys := make([]string, 0, len(req.Args))
	for k := range req.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	service := req.Args["service"]
	repo := service + "-controller"
	digest, err := requestDigest(req, timeout)
	if err != nil {
		return nil, err
	}

	env := []EnvVar{
		{Name: "WORKFLOW_NAME", Value: req.Workflow},
		{Name: "ISSUE_NUMBER", Value: "0"},
		{Name: "REPO_OWNER", Value: cfg.Org},
		{Name: "REPO_NAME", Value: repo},
		{Name: "ISSUE_AUTHOR", Value: "ack-autopilot"},
	}
	env = append(env, sortedEnv(w.Environment)...)
	for _, name := range sortedKeys(w.EnvironmentFromSecrets) {
		ref := w.EnvironmentFromSecrets[name]
		env = append(env, EnvVar{Name: name, ValueFrom: &EnvVarSource{SecretKeyRef: &SecretKeySelector{Name: ref.Name, Key: ref.Key}}})
	}
	refs, refEnv := extraRefs(w.ExtraRefs)
	env = append(env, refEnv...)
	var args []string
	var pairs []string
	for _, k := range keys {
		pairs = append(pairs, k+"="+req.Args[k])
		if w.pod.ArgEnv == nil {
			args = append(args, "--"+k, req.Args[k])
		} else if name, ok := w.pod.ArgEnv[k]; ok {
			env = append(env, EnvVar{Name: name, Value: req.Args[k]})
		}
	}

	container := Container{
		Name: "workflow-runner", Image: w.Image, Command: w.Command, Args: args, Env: env,
		VolumeMounts: []VolumeMount{{Name: w.pod.SecretsVolume, MountPath: w.pod.SecretsMount, ReadOnly: true}},
	}
	if r := w.Resources; r != nil {
		for _, q := range []string{r.CPU, r.Memory} {
			if q != "" && !quantityRE.MatchString(q) {
				return nil, fmt.Errorf("resource quantity %q is not canonical", q)
			}
		}
		lim := map[string]string{}
		if r.CPU != "" {
			lim["cpu"] = r.CPU
		}
		if r.Memory != "" {
			lim["memory"] = r.Memory
		}
		if len(lim) > 0 {
			req := map[string]string{}
			for k, v := range lim {
				req[k] = v
			}
			container.Resources = ResourceRequirements{Limits: lim, Requests: req}
		}
	}
	pod := &PodSpec{
		RestartPolicy: "Never", ServiceAccountName: w.pod.ServiceAccount,
		Containers: []Container{container},
		Volumes: []Volume{{Name: w.pod.SecretsVolume, CSI: &CSISource{
			Driver: "secrets-store.csi.k8s.io", ReadOnly: boolPtr(true),
			VolumeAttributes: map[string]string{"secretProviderClass": w.pod.SecretProviderClass},
		}}},
	}
	if w.E2E {
		applyE2E(pod)
	}

	labels := map[string]string{
		LabelWorkflow:                 req.Workflow,
		"triggered-by":                "workflow-agent",
		"prow.k8s.io/type":            "periodic",
		"prow.k8s.io/job":             job,
		"prow.k8s.io/refs.org":        cfg.Org,
		"prow.k8s.io/refs.repo":       repo,
		"created-by-prow":             "true",
		"app.kubernetes.io/name":      job,
		"workflow-agent/issue-number": "0",
		LabelManagedBy:                ManagedBy,
		LabelKeyHash:                  hash,
		LabelGeneration:               gen,
		LabelRun:                      run,
		LabelService:                  service,
	}
	annotations := map[string]string{
		"workflow-agent/workflow-name": req.Workflow,
		"workflow-agent/issue-number":  "0",
		"workflow-agent/command-args":  strings.Join(pairs, ","),
		"prow.k8s.io/job":              job,
		"prow.k8s.io/refs.org":         cfg.Org,
		"prow.k8s.io/refs.repo":        repo,
		"prow.k8s.io/refs.pull":        "0",
		AnnotationRequest:              digest,
		AnnotationWorkItem:             "sha256:" + hash,
	}
	ui := utilityImages
	return &ProwJob{
		APIVersion: "prow.k8s.io/v1",
		Kind:       "ProwJob",
		Metadata:   ObjectMeta{Name: req.LaunchID, Namespace: Namespace, Labels: labels, Annotations: annotations},
		Spec: Spec{
			Type: "periodic", Agent: "kubernetes", Cluster: w.pod.Cluster, Job: job, ExtraRefs: refs,
			DecorationConfig: &DecorationConfig{
				Timeout:          timeout.String(),
				GracePeriod:      (15 * time.Minute).String(),
				UtilityImages:    &ui,
				GCSConfiguration: &GCSConfiguration{Bucket: cfg.Bucket, PathStrategy: "explicit"},
				CensorSecrets:    boolPtr(true),
				CensoringOptions: &CensoringOptions{IncludeDirectories: []string{"/etc/github"}},
			},
			PodSpec: pod,
		},
		Status: JobStatus{
			StartTime:   cfg.Now().UTC().Format(time.RFC3339),
			State:       "triggered",
			Description: "Job triggered by workflow-agent",
		},
	}, nil
}

// requestDigest is the SHA-256 of the canonical request: what Launch compares
// before it adopts a job that already exists.
func requestDigest(req dispatcher.LaunchRequest, timeout time.Duration) (string, error) {
	b, err := json.Marshal(struct {
		LaunchID string            `json:"launch_id"`
		Workflow string            `json:"workflow"`
		Args     map[string]string `json:"args"`
		Timeout  string            `json:"timeout"`
	}{req.LaunchID, req.Workflow, req.Args, timeout.String()})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// clonerefsSrcRoot is where clonerefs checks refs out (agent-plugin's).
const clonerefsSrcRoot = "/home/prow/go/src"

// extraRefs is agent-plugin's buildExtraRefs, plus workdir.
func extraRefs(refs []ExtraRef) ([]Refs, []EnvVar) {
	var out []Refs
	var env []EnvVar
	for _, r := range refs {
		base := r.BaseRef
		if base == "" {
			base = "main"
		}
		alias := r.PathAlias
		if alias == "" {
			alias = fmt.Sprintf("github.com/%s/%s", r.Org, r.Repo)
		}
		out = append(out, Refs{Org: r.Org, Repo: r.Repo, BaseRef: base, PathAlias: alias, WorkDir: r.WorkDir})
		if r.Env != "" {
			env = append(env, EnvVar{Name: r.Env, Value: clonerefsSrcRoot + "/" + alias})
		}
	}
	return out, env
}

// applyE2E is agent-plugin's applyE2EPodSettings: kind in Docker in Docker.
func applyE2E(pod *PodSpec) {
	c := &pod.Containers[0]
	c.SecurityContext = &SecurityContext{Privileged: boolPtr(true)}
	c.Env = append(c.Env, EnvVar{Name: "DOCKER_IN_DOCKER_ENABLED", Value: "true"}, EnvVar{Name: "RUN_E2E", Value: "true"})
	pod.Volumes = append(pod.Volumes,
		Volume{Name: "docker-graph", EmptyDir: &EmptyDirSource{}},
		Volume{Name: "docker-root", EmptyDir: &EmptyDirSource{}},
		Volume{Name: "modules", HostPath: &HostPathSource{Path: "/lib/modules", Type: "Directory"}},
		Volume{Name: "cgroup", HostPath: &HostPathSource{Path: "/sys/fs/cgroup", Type: "Directory"}},
	)
	c.VolumeMounts = append(c.VolumeMounts,
		VolumeMount{Name: "docker-graph", MountPath: "/docker-graph"},
		VolumeMount{Name: "docker-root", MountPath: "/var/lib/docker"},
		VolumeMount{Name: "modules", MountPath: "/lib/modules", ReadOnly: true},
		VolumeMount{Name: "cgroup", MountPath: "/sys/fs/cgroup"},
	)
}

func sortedEnv(m map[string]string) []EnvVar {
	out := make([]EnvVar, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, EnvVar{Name: k, Value: m[k]})
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func boolPtr(b bool) *bool { return &b }
