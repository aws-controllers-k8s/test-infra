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
	"encoding/json"
	"fmt"
	"log"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// Implementation of the Client interface using Kubernetes API
type k8sClient struct {
	dynamicClient dynamic.Interface
}

// NewClient creates a new Kubernetes client
func NewClient(dynamicClient dynamic.Interface) Client {
	return &k8sClient{
		dynamicClient: dynamicClient,
	}
}

// SubmitProwJob submits a ProwJob to the Kubernetes cluster
func (c *k8sClient) SubmitProwJob(ctx context.Context, prowJob *ProwJob, namespace string) error {
	if c.dynamicClient == nil {
		return fmt.Errorf("kubernetes client not initialized")
	}

	log.Printf("Submitting ProwJob to Kubernetes: name=%s, namespace=%s",
		prowJob.Name, namespace)

	prowJobMap, err := prowJobToUnstructured(prowJob)
	if err != nil {
		return fmt.Errorf("failed to convert ProwJob to unstructured: %w", err)
	}

	// Create the ProwJob using dynamic client
	_, err = c.dynamicClient.Resource(GetProwJobGVR()).Namespace(namespace).Create(
		ctx, prowJobMap, GetCreateOptions())
	if err != nil {
		return fmt.Errorf("failed to create ProwJob: %w", err)
	}

	log.Printf("Successfully submitted ProwJob: name=%s, namespace=%s",
		prowJob.Name, namespace)
	return nil
}

// prowJobToUnstructured converts a ProwJob to unstructured format for dynamic client
func prowJobToUnstructured(prowJob *ProwJob) (*unstructured.Unstructured, error) {
	jobBytes, err := json.Marshal(prowJob)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ProwJob: %w", err)
	}

	var obj map[string]interface{}
	if err := json.Unmarshal(jobBytes, &obj); err != nil {
		return nil, fmt.Errorf("failed to unmarshal to map: %w", err)
	}

	return &unstructured.Unstructured{Object: obj}, nil
}
