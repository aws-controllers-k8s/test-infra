package prowjob

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/prow/pkg/github"
	prowv1 "sigs.k8s.io/prow/pkg/apis/prowjobs/v1"

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
	) (*k8s.ProwJob, error)
}

// DefaultGenerator is the standard implementation of the Generator interface
type DefaultGenerator struct {
	workflows map[string]*Workflow
}

// NewGenerator creates a new ProwJob generator
func NewGenerator(workflows map[string]*Workflow) Generator {
	return &DefaultGenerator{workflows: workflows}
}

// CreateWorkflowProwJob creates a ProwJob for a workflow execution
func (g *DefaultGenerator) CreateWorkflowProwJob(
	workflowName string,
	args map[string]string,
	flags []string,
	issue github.Issue,
	repo github.Repo,
	timeout string,
) (*k8s.ProwJob, error) {

	workflow, exists := g.workflows[workflowName]
	if !exists {
		return nil, fmt.Errorf("workflow %s not found", workflowName)
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
			Namespace: "prow-jobs",
			Labels: map[string]string{
				"workflow-type":         workflowName,
				"triggered-by":          "workflow-agent",
				"prow.k8s.io/type":      "periodic",
				"prow.k8s.io/job":       fmt.Sprintf("agent-workflow-%s", workflowName),
				"prow.k8s.io/refs.org":  repo.Owner.Login,
				"prow.k8s.io/refs.repo": repo.Name,
				"created-by-prow":       "true", // Required label by Prow
				"app.kubernetes.io/name": fmt.Sprintf("agent-workflow-%s", workflowName),
			},
			Annotations: map[string]string{
				"workflow-agent/workflow-name": workflowName,
				"workflow-agent/issue-number":  strconv.Itoa(issue.Number),
				"workflow-agent/command-args":  mapToString(args),
				"prow.k8s.io/job": fmt.Sprintf("agent-workflow-%s", workflowName),
				"prow.k8s.io/refs.org": repo.Owner.Login,
				"prow.k8s.io/refs.repo": repo.Name,
				"prow.k8s.io/refs.pull": strconv.Itoa(issue.Number),
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
			Cluster: "default",
			Job:     fmt.Sprintf("agent-workflow-%s", workflowName),
			// Add decoration config for S3 logs
			DecorationConfig: &k8s.DecorationConfig{
				Timeout:    &prowv1.Duration{Duration: timeoutDuration},
				GracePeriod: &prowv1.Duration{Duration: 15 * time.Minute},
				GCSConfiguration: &k8s.GCSConfiguration{
					Bucket:       "s3://ack-prow-staging-artifacts",
					PathStrategy: "explicit",
				},
				S3CredentialsSecret: String("s3-credentials"),
				UtilityImages: &k8s.UtilityImages{
					CloneRefs:  "us-docker.pkg.dev/k8s-infra-prow/images/clonerefs:v20240802-66b115076",
					InitUpload: "us-docker.pkg.dev/k8s-infra-prow/images/initupload:v20240802-66b115076",
					Entrypoint: "us-docker.pkg.dev/k8s-infra-prow/images/entrypoint:v20240802-66b115076",
					Sidecar:    "us-docker.pkg.dev/k8s-infra-prow/images/sidecar:v20240802-66b115076",
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
					VolumeMounts: []v1.VolumeMount{
						{
							Name:      "jobs-config",
							MountPath: "/prow/jobs",
							ReadOnly:  true,
						},
					},
				}},
				Volumes: []v1.Volume{
					{
						Name: "jobs-config",
						VolumeSource: v1.VolumeSource{
							ConfigMap: &v1.ConfigMapVolumeSource{
								LocalObjectReference: v1.LocalObjectReference{
									Name: "jobs-config",
								},
							},
						},
					},
				},
			},
		},
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
