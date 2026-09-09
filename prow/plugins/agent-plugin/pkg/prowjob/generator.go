// Copyright 2020 Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package prowjob

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	prowv1 "sigs.k8s.io/prow/pkg/apis/prowjobs/v1"
	"sigs.k8s.io/prow/pkg/github"

	"github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/k8s"
)

// Generator creates ProwJobs for workflows
type Generator interface {
	CreateWorkflowProwJob(
		workflowName string,
		args map[string]string,
		flags []string,
		issue github.Issue,
		repo github.Repo,
		timeout string,
		namespace string,
		s3BucketName string,
	) (*k8s.ProwJob, error)
}

// DefaultGenerator is the standard implementation of the Generator interface.
// It holds a WorkflowConfigLoader rather than a pre-parsed map so per-request
// lookups pick up ConfigMap updates without a pod restart.
type DefaultGenerator struct {
	loader *WorkflowConfigLoader
}

// NewGenerator creates a new ProwJob generator backed by the given loader.
func NewGenerator(loader *WorkflowConfigLoader) Generator {
	return &DefaultGenerator{loader: loader}
}

// CreateWorkflowProwJob creates a ProwJob for a workflow execution
func (g *DefaultGenerator) CreateWorkflowProwJob(
	workflowName string,
	args map[string]string,
	flags []string,
	issue github.Issue,
	repo github.Repo,
	timeout string,
	namespace string,
	s3Bucket string,
) (*k8s.ProwJob, error) {

	// Look up the workflow through the loader on every call — this is what
	// makes the plugin pick up an updated agent-workflow-config ConfigMap
	// (e.g. a bumped image tag) without a pod restart.
	workflow, err := g.loader.GetWorkflowByName(workflowName)
	if err != nil {
		return nil, err
	}

	envVars := []v1.EnvVar{
		{Name: "WORKFLOW_NAME", Value: workflowName},
		{Name: "ISSUE_NUMBER", Value: strconv.Itoa(issue.Number)},
		{Name: "REPO_OWNER", Value: repo.Owner.Login},
		{Name: "REPO_NAME", Value: repo.Name},
		{Name: "ISSUE_AUTHOR", Value: issue.User.Login},
	}

	// Add workflow-specific environment variables
	for key, value := range workflow.Environment {
		envVars = append(envVars, v1.EnvVar{
			Name:  key,
			Value: value,
		})
	}

	for key, secretRef := range workflow.EnvironmentFromSecrets {
		envVars = append(envVars, v1.EnvVar{
			Name: key,
			ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					LocalObjectReference: v1.LocalObjectReference{
						Name: secretRef.Name,
					},
					Key: secretRef.Key,
				},
			},
		})
	}

	// Translate the workflow's declared stable repo dependencies into Prow
	// extra_refs (cloned by the clonerefs init container) and inject each ref's
	// checkout path as an env var where requested, so the workflow reads the
	// exact path the repo is cloned to.
	extraRefs, refEnvVars := buildExtraRefs(workflow.ExtraRefs)
	envVars = append(envVars, refEnvVars...)

	// Add arguments as command-line flags
	workflowArgs := make([]string, 0)
	for key, value := range args {
		workflowArgs = append(workflowArgs, fmt.Sprintf("--%s", key))
		workflowArgs = append(workflowArgs, value)
	}

	// Add any additional flags
	workflowArgs = append(workflowArgs, flags...)

	// Generate unique job ID
	jobID := generateJobID()
	// Use only lowercase alphanumerics and hyphens for job name (DNS-1123 format)
	safeWorkflowName := strings.ReplaceAll(strings.ToLower(workflowName), "_", "-")
	jobName := fmt.Sprintf("periodic-agent-%s-%s", safeWorkflowName, jobID)

	// Parse timeout duration for ProwJob
	timeoutDuration, err := parseTimeout(timeout, workflow.TimeoutDur)
	if err != nil {
		return nil, fmt.Errorf("invalid timeout: %w", err)
	}

	prowJob := &k8s.ProwJob{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "prow.k8s.io/v1",
			Kind:       "ProwJob",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: namespace,
			Labels: map[string]string{
				"workflow-type":          workflowName,
				"triggered-by":           "workflow-agent",
				"prow.k8s.io/type":       "periodic",
				"prow.k8s.io/job":        fmt.Sprintf("agent-workflow-%s", workflowName),
				"prow.k8s.io/refs.org":   repo.Owner.Login,
				"prow.k8s.io/refs.repo":  repo.Name,
				"created-by-prow":        "true", // Required label by Prow
				"app.kubernetes.io/name": fmt.Sprintf("agent-workflow-%s", workflowName),
			},
			Annotations: map[string]string{
				"workflow-agent/workflow-name": workflowName,
				"workflow-agent/issue-number":  strconv.Itoa(issue.Number),
				"workflow-agent/command-args":  mapToString(args),
				"prow.k8s.io/job":              fmt.Sprintf("agent-workflow-%s", workflowName),
				"prow.k8s.io/refs.org":         repo.Owner.Login,
				"prow.k8s.io/refs.repo":        repo.Name,
				"prow.k8s.io/refs.pull":        strconv.Itoa(issue.Number),
			},
		},
		Status: k8s.ProwJobStatus{
			StartTime:   metav1.Now(),
			State:       k8s.TriggeredState,
			Description: "Job triggered by workflow-agent",
		},
		Spec: k8s.ProwJobSpec{
			Type:    k8s.PeriodicJob,
			Agent:   k8s.KubernetesAgent,
			// Run on the dedicated build cluster for workload isolation from the
			// Prow control plane. "build" is the kubeconfig context alias Prow
			// registers for the build cluster (see jobs_config.yaml presubmit_cluster).
			Cluster: "build",
			Job:     fmt.Sprintf("agent-workflow-%s", workflowName),
			// Stable repo dependencies (code-generator, runtime, ack-dev-skills,
			// ...) cloned into the pod by the clonerefs init container. Empty when
			// the workflow declares no extra_refs.
			ExtraRefs: extraRefs,
			// Add decoration config for S3 logs
			DecorationConfig: &k8s.DecorationConfig{
				Timeout:     &prowv1.Duration{Duration: timeoutDuration},
				GracePeriod: &prowv1.Duration{Duration: 15 * time.Minute},
				GCSConfiguration: &k8s.GCSConfiguration{
					Bucket:       s3Bucket,
					PathStrategy: "explicit",
				},
				CensorSecrets: Bool(true),
				CensoringOptions: &prowv1.CensoringOptions{
					IncludeDirectories: []string{"/etc/github"},
				},
				UtilityImages: &k8s.UtilityImages{
					CloneRefs:  "public.ecr.aws/eks-distro-build-tooling/prow-clonerefs:v20260316-26fa34da6",
					InitUpload: "public.ecr.aws/eks-distro-build-tooling/prow-initupload:v20260316-26fa34da6",
					Entrypoint: "public.ecr.aws/eks-distro-build-tooling/prow-entrypoint:v20260316-26fa34da6",
					Sidecar:    "public.ecr.aws/eks-distro-build-tooling/prow-sidecar:v20260316-26fa34da6",
				},
			},
			PodSpec: &v1.PodSpec{
				RestartPolicy:      v1.RestartPolicyNever,
				ServiceAccountName: "workflow-runner",
				Containers: []v1.Container{{
					Name:    "workflow-runner",
					Image:   workflow.Image,
					Command: workflow.Command,
					Args:    workflowArgs,
					Env:     envVars,
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{},
						Limits:   v1.ResourceList{},
					},
					// Mount the SecretProviderClass so the Secrets Store CSI driver
					// syncs the dedicated agent GitHub PAT (agent-github-pat-token)
					// into a Kubernetes Secret using this pod's workflow-runner Pod
					// Identity. GITHUB_TOKEN then reads that Secret via secretKeyRef.
					VolumeMounts: []v1.VolumeMount{
						{
							Name:      "agent-secrets",
							MountPath: "/mnt/secrets-store",
							ReadOnly:  true,
						},
					},
				}},
				Volumes: []v1.Volume{
					{
						Name: "agent-secrets",
						VolumeSource: v1.VolumeSource{
							CSI: &v1.CSIVolumeSource{
								Driver:   "secrets-store.csi.k8s.io",
								ReadOnly: Bool(true),
								VolumeAttributes: map[string]string{
									"secretProviderClass": "agent-secrets",
								},
							},
						},
					},
				},
			},
		},
	}

	// For e2e-enabled workflows, provision the pod for kind-in-Docker-in-Docker.
	// Provision the pod for kind-in-Docker-in-Docker when the workflow opts in.
	if workflow.E2E {
		applyE2EPodSettings(prowJob.Spec.PodSpec)
	}

	// Set resource limits if specified
	if workflow.Resources != nil {
		container := &prowJob.Spec.PodSpec.Containers[0]
		if workflow.Resources.CPU != "" {
			if container.Resources.Limits == nil {
				container.Resources.Limits = v1.ResourceList{}
			}
			if container.Resources.Requests == nil {
				container.Resources.Requests = v1.ResourceList{}
			}
			container.Resources.Limits[v1.ResourceCPU] = parseResourceQuantity(workflow.Resources.CPU)
			container.Resources.Requests[v1.ResourceCPU] = parseResourceQuantity(workflow.Resources.CPU)
		}
		if workflow.Resources.Memory != "" {
			if container.Resources.Limits == nil {
				container.Resources.Limits = v1.ResourceList{}
			}
			if container.Resources.Requests == nil {
				container.Resources.Requests = v1.ResourceList{}
			}
			container.Resources.Limits[v1.ResourceMemory] = parseResourceQuantity(workflow.Resources.Memory)
			container.Resources.Requests[v1.ResourceMemory] = parseResourceQuantity(workflow.Resources.Memory)
		}
	}

	return prowJob, nil
}

// applyE2EPodSettings provisions the pod for kind-in-Docker-in-Docker. It is the
// inline equivalent of the preset-dind-enabled and preset-kind-volume-mounts presets
// (prow/config/templates/config-ConfigMap.yaml) plus the privileged securityContext
// the integration-test jobs set inline. preset-test-config is not mirrored: roles/e2e.py
// writes its own test_config.yaml. Safe to call on the single-container PodSpec
// CreateWorkflowProwJob builds.
func applyE2EPodSettings(podSpec *v1.PodSpec) {
	if podSpec == nil || len(podSpec.Containers) == 0 {
		return
	}
	c := &podSpec.Containers[0]

	// Privileged is required for dockerd + kind node containers to run.
	c.SecurityContext = &v1.SecurityContext{Privileged: Bool(true)}

	// preset-dind-enabled: signal DinD to the harness and give dockerd its
	// storage. docker-root (/var/lib/docker) is load-bearing for a modern
	// dockerd; docker-graph is the legacy kubekins path, kept for fidelity.
	c.Env = append(c.Env,
		v1.EnvVar{Name: "DOCKER_IN_DOCKER_ENABLED", Value: "true"},
		// Gate the e2e path in the workflow (roles/config.py reads this).
		v1.EnvVar{Name: "RUN_E2E", Value: "true"},
	)

	hostPathDir := v1.HostPathDirectory
	podSpec.Volumes = append(podSpec.Volumes,
		v1.Volume{Name: "docker-graph", VolumeSource: v1.VolumeSource{EmptyDir: &v1.EmptyDirVolumeSource{}}},
		v1.Volume{Name: "docker-root", VolumeSource: v1.VolumeSource{EmptyDir: &v1.EmptyDirVolumeSource{}}},
		// preset-kind-volume-mounts: kind's nodes need the host's kernel modules
		// and cgroup hierarchy.
		v1.Volume{Name: "modules", VolumeSource: v1.VolumeSource{HostPath: &v1.HostPathVolumeSource{Path: "/lib/modules", Type: &hostPathDir}}},
		v1.Volume{Name: "cgroup", VolumeSource: v1.VolumeSource{HostPath: &v1.HostPathVolumeSource{Path: "/sys/fs/cgroup", Type: &hostPathDir}}},
	)
	c.VolumeMounts = append(c.VolumeMounts,
		v1.VolumeMount{Name: "docker-graph", MountPath: "/docker-graph"},
		v1.VolumeMount{Name: "docker-root", MountPath: "/var/lib/docker"},
		v1.VolumeMount{Name: "modules", MountPath: "/lib/modules", ReadOnly: true},
		v1.VolumeMount{Name: "cgroup", MountPath: "/sys/fs/cgroup"},
	)
}

// clonerefsSrcRoot is where Prow's clonerefs init container checks out refs
// under the decorated pod's shared code volume (the upstream default GOPATH src
// root). Extra-ref checkout paths are computed relative to it.
const clonerefsSrcRoot = "/home/prow/go/src"

// buildExtraRefs converts a workflow's declared stable repo dependencies into
// Prow extra_refs and the env vars that expose each ref's checkout path. The
// checkout path and the ref's PathAlias are derived from the same inputs, so the
// path the workflow reads cannot drift from where clonerefs clones the repo.
func buildExtraRefs(refs []ExtraRef) ([]prowv1.Refs, []v1.EnvVar) {
	if len(refs) == 0 {
		return nil, nil
	}
	prowRefs := make([]prowv1.Refs, 0, len(refs))
	envVars := make([]v1.EnvVar, 0, len(refs))
	for _, r := range refs {
		baseRef := r.BaseRef
		if baseRef == "" {
			baseRef = "main"
		}
		pathAlias := r.PathAlias
		if pathAlias == "" {
			pathAlias = fmt.Sprintf("github.com/%s/%s", r.Org, r.Repo)
		}
		prowRefs = append(prowRefs, prowv1.Refs{
			Org:       r.Org,
			Repo:      r.Repo,
			BaseRef:   baseRef,
			PathAlias: pathAlias,
		})
		if r.Env != "" {
			envVars = append(envVars, v1.EnvVar{
				Name:  r.Env,
				Value: fmt.Sprintf("%s/%s", clonerefsSrcRoot, pathAlias),
			})
		}
	}
	return prowRefs, envVars
}
