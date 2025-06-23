package prowjob

import (
	"fmt"
	"strconv"
	"strings"
	
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/github"
	"github.com/aws-controllers-k8s/test-infra/experimental/prow/pkg/k8s"
)

// Generator creates ProwJobs for workflows
type Generator interface {
	CreateWorkflowProwJob(
		workflowName string,
		args map[string]string,
		flags []string,
		issue *github.Issue,
		repo *github.Repository,
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
	issue *github.Issue,
	repo *github.Repository,
	timeout string,
) (*k8s.ProwJob, error) {

	workflow, exists := g.workflows[workflowName]
	if !exists {
		return nil, fmt.Errorf("workflow %s not found", workflowName)
	}

	envVars := []v1.EnvVar{
		{Name: "WORKFLOW_NAME", Value: workflowName},
		{Name: "ISSUE_NUMBER", Value: strconv.Itoa(issue.GetNumber())},
		{Name: "REPO_OWNER", Value: repo.GetOwner().GetLogin()},
		{Name: "REPO_NAME", Value: repo.GetName()},
		{Name: "ISSUE_AUTHOR", Value: issue.GetUser().GetLogin()},
	}

	// Add workflow-specific environment variables
	for key, value := range workflow.Environment {
		envVars = append(envVars, v1.EnvVar{
			Name:  key,
			Value: value,
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
	_, err := parseTimeout(timeout, workflow.TimeoutDur)
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
				"workflow-type":        workflowName,
				"triggered-by":         "workflow-agent",
				"prow.k8s.io/type":     "periodic",
				"prow.k8s.io/job":      fmt.Sprintf("agent-workflow-%s", workflowName),
				"prow.k8s.io/refs.org": repo.GetOwner().GetLogin(),
				"prow.k8s.io/refs.repo": repo.GetName(),
				"created-by-prow":       "true", // Required label by Prow
			},
			Annotations: map[string]string{
				"workflow-agent/workflow-name": workflowName,
				"workflow-agent/issue-number":  strconv.Itoa(issue.GetNumber()),
				"workflow-agent/command-args":  mapToString(args),
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
			// No decoration config for periodic jobs
			PodSpec: &v1.PodSpec{
				RestartPolicy: v1.RestartPolicyNever,
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
							Name:      "github-app",
							MountPath: "/etc/github",
							ReadOnly:  true,
						},
						{
							Name:      "jobs-config",
							MountPath: "/prow/jobs",
							ReadOnly:  true,
						},
					},
				}},
				Volumes: []v1.Volume{
					{
						Name: "github-app",
						VolumeSource: v1.VolumeSource{
							Secret: &v1.SecretVolumeSource{
								SecretName: "github-app-files",
							},
						},
					},
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