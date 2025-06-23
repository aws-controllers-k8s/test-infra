package k8s

import (
	"context"
	
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Client defines the interface for Kubernetes operations
type Client interface {
	// ProwJob operations
	SubmitProwJob(ctx context.Context, prowJob *ProwJob, namespace string) error
}


// GetProwJobGVR returns the GroupVersionResource for ProwJobs
func GetProwJobGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "prow.k8s.io",
		Version:  "v1",
		Resource: "prowjobs",
	}
}

// GetCreateOptions returns standard metav1.CreateOptions
func GetCreateOptions() metav1.CreateOptions {
	return metav1.CreateOptions{}
}