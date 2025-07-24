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
