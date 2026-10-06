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

package command

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewestSemverRefsOrdersAndBounds(t *testing.T) {
	refs := []string{
		"refs/tags/service/s3/v1.9.0",
		"refs/tags/service/s3/v1.100.0",
		"refs/tags/service/s3/v1.100.1-rc.1",
		"refs/tags/service/s3/v1.99.0",
		"refs/tags/service/s3/vNotASemver",
		"refs/tags/service/s3/v1.10.0",
	}
	got, err := newestSemverRefs(refs, 3)
	require.NoError(t, err)
	assert.Equal(t, []string{"v1.100.0", "v1.99.0", "v1.10.0"}, got,
		"highest first, numeric not lexical, prereleases and junk skipped, bounded to n")

	got, err = newestSemverRefs(refs[:2], 3)
	require.NoError(t, err)
	assert.Equal(t, []string{"v1.100.0", "v1.9.0"}, got, "fewer than n is not an error")
}

// modelTransport serves models by URL path (unknown paths 404) and records every request.
type modelTransport struct {
	models   map[string]string
	statuses map[string]int
	requests *[]string
}

func (m modelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	*m.requests = append(*m.requests, req.URL.Path)
	status, body := http.StatusNotFound, ""
	if file, ok := m.models[req.URL.Path]; ok {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		status, body = http.StatusOK, string(data)
	} else if s, ok := m.statuses[req.URL.Path]; ok {
		status = s
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
		Request:    req,
	}, nil
}

// withModelTransport points modelHTTPClient at a modelTransport and returns the request log.
func withModelTransport(t *testing.T, models map[string]string, statuses map[string]int) *[]string {
	t.Helper()
	var requests []string
	original := modelHTTPClient
	modelHTTPClient = &http.Client{Transport: modelTransport{models, statuses, &requests}}
	t.Cleanup(func() { modelHTTPClient = original })
	return &requests
}

func demoModelPath(version string) string {
	return "/aws/aws-sdk-go-v2/service/demoservice/" + version +
		"/codegen/sdk-codegen/aws-models/demo-service.json"
}

func TestLatestModelSkipsATagWithoutTheModel(t *testing.T) {
	// A module tag can exist without the model file; fall back to the next tag that has it.
	requests := withModelTransport(t,
		map[string]string{demoModelPath("v1.2.0"): "../../../testdata/smithy_basic.json"}, nil)
	_ = captureLog(t)

	version, model, found, err := latestModel(context.Background(), t.TempDir(),
		"demo-service", "demoservice", "v1.0.0", []string{"v1.3.0", "v1.2.0", "v1.1.0"})
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "v1.2.0", version)
	assert.Equal(t, []string{"CreateWidget", "DeleteWidget"}, model.OperationNames())
	assert.Equal(t, []string{demoModelPath("v1.3.0"), demoModelPath("v1.2.0")}, *requests,
		"stops at the first candidate with the model")
}

func TestLatestModelStopsAtTheReleaseVersion(t *testing.T) {
	// The controller already ships the newest SDK with the model, so nothing is fetched.
	requests := withModelTransport(t, nil, nil)
	_ = captureLog(t)

	version, model, found, err := latestModel(context.Background(), t.TempDir(),
		"demo-service", "demoservice", "v1.2.0", []string{"v1.3.0", "v1.2.0", "v1.1.0"})
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, model)
	assert.Equal(t, "v1.2.0", version)
	assert.Equal(t, []string{demoModelPath("v1.3.0")}, *requests)
}

func TestLatestModelDoesNotSkipOtherFailures(t *testing.T) {
	// Only a 404 means the model is absent; a 500 must not step back to an older model.
	requests := withModelTransport(t,
		map[string]string{demoModelPath("v1.2.0"): "../../../testdata/smithy_basic.json"},
		map[string]int{demoModelPath("v1.3.0"): http.StatusInternalServerError})

	_, _, found, err := latestModel(context.Background(), t.TempDir(),
		"demo-service", "demoservice", "v1.0.0", []string{"v1.3.0", "v1.2.0"})
	require.Error(t, err)
	assert.False(t, found)
	assert.NotErrorIs(t, err, errModelNotFound)
	assert.Equal(t, []string{demoModelPath("v1.3.0")}, *requests)
}

func TestLatestModelFailsWhenNoCandidateHasTheModel(t *testing.T) {
	requests := withModelTransport(t, nil, nil)
	_ = captureLog(t)

	_, _, found, err := latestModel(context.Background(), t.TempDir(),
		"demo-service", "demoservice", "v1.0.0", []string{"v1.3.0", "v1.2.0"})
	require.ErrorContains(t, err, "none of the 2 newest service/demoservice tags")
	assert.False(t, found)
	assert.Len(t, *requests, 2, "each candidate is tried once and no further")
}

func TestHTTPGetMarksANotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing.json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := httpGet(context.Background(), srv.URL+"/missing.json")
	assert.ErrorIs(t, err, errModelNotFound)

	_, err = httpGet(context.Background(), srv.URL+"/flaky.json")
	require.Error(t, err)
	assert.NotErrorIs(t, err, errModelNotFound, "only a 404 means the model is absent")
}

func TestNewGithubClientFromEnvBoundsEachRequest(t *testing.T) {
	// go-github's default client has no timeout, so a stalled response would hang the job.
	t.Setenv("GITHUB_TOKEN", "not-a-real-token")
	client, err := newGithubClientFromEnv()
	require.NoError(t, err)
	assert.Equal(t, githubRequestTimeout, client.Client().Timeout)
}
