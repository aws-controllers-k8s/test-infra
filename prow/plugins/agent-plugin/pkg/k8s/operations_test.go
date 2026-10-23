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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// TestListProwJobs submits ProwJobs through the client and lists them back
// with a label selector, covering the unstructured round trip.
func TestListProwJobs(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{GetProwJobGVR(): "ProwJobList"},
	)
	c := NewClient(dyn)
	ctx := context.Background()

	now := metav1.Now()
	jobs := []*ProwJob{
		{ObjectMeta: metav1.ObjectMeta{Name: "pr-42-running", Labels: map[string]string{"pr": "42"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pr-42-done", Labels: map[string]string{"pr": "42"}},
			Status: ProwJobStatus{State: SuccessState, CompletionTime: &now}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pr-7", Labels: map[string]string{"pr": "7"}}},
	}
	for _, j := range jobs {
		j.APIVersion = "prow.k8s.io/v1"
		j.Kind = "ProwJob"
		if err := c.SubmitProwJob(ctx, j, "prow-jobs"); err != nil {
			t.Fatalf("SubmitProwJob(%s): %v", j.Name, err)
		}
	}

	got, err := c.ListProwJobs(ctx, "prow-jobs", "pr=42")
	if err != nil {
		t.Fatalf("ListProwJobs: %v", err)
	}
	complete := map[string]bool{}
	for i := range got {
		complete[got[i].Name] = got[i].Complete()
	}
	want := map[string]bool{"pr-42-running": false, "pr-42-done": true}
	if len(complete) != len(want) {
		t.Fatalf("listed %v, want %v", complete, want)
	}
	for name, done := range want {
		if c, ok := complete[name]; !ok || c != done {
			t.Errorf("job %s: listed=%v complete=%v, want complete=%v", name, ok, c, done)
		}
	}
}
