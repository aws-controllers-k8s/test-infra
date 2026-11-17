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

package prowlaunch

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testServer = "https://10.0.0.1:443"

// fakeAPI is an in-memory ProwJob API behind an http.RoundTripper: no
// socket, no listener. It checks the bearer token and keeps resourceVersion.
type fakeAPI struct {
	mu       sync.Mutex
	token    string
	jobs     map[string]map[string]any
	rv       int
	requests []string
	// status, when set, answers every request with it.
	status int
	body   string
}

func newFakeAPI(token string) *fakeAPI {
	return &fakeAPI{token: token, jobs: map[string]map[string]any{}}
}

func reply(code int, v any) *http.Response {
	b, _ := json.Marshal(v)
	return &http.Response{StatusCode: code, Body: io.NopCloser(bytes.NewReader(b)), Header: http.Header{}}
}

func (f *fakeAPI) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		return reply(401, map[string]any{"reason": "Unauthorized", "message": "bad token"}), nil
	}
	if f.status != 0 {
		return &http.Response{StatusCode: f.status, Body: io.NopCloser(strings.NewReader(f.body)), Header: http.Header{}}, nil
	}
	const base = "/apis/prow.k8s.io/v1/namespaces/prow/prowjobs"
	name := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, base), "/")
	var body map[string]any
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		if len(b) > 0 {
			if err := json.Unmarshal(b, &body); err != nil {
				return reply(400, map[string]any{"message": err.Error()}), nil
			}
		}
	}
	switch {
	case r.Method == http.MethodPost && name == "":
		n := body["metadata"].(map[string]any)["name"].(string)
		if _, ok := f.jobs[n]; ok {
			return reply(409, map[string]any{"reason": "AlreadyExists"}), nil
		}
		f.rv++
		body["metadata"].(map[string]any)["resourceVersion"] = strconv.Itoa(f.rv)
		f.jobs[n] = body
		return reply(201, body), nil
	case r.Method == http.MethodGet && name == "":
		sel := r.URL.Query().Get("labelSelector")
		k, v, _ := strings.Cut(sel, "=")
		var items []any
		for _, j := range f.jobs {
			labels, _ := j["metadata"].(map[string]any)["labels"].(map[string]any)
			if labels[k] == v {
				items = append(items, j)
			}
		}
		return reply(200, map[string]any{"metadata": map[string]any{}, "items": items}), nil
	case r.Method == http.MethodGet:
		j, ok := f.jobs[name]
		if !ok {
			return reply(404, map[string]any{"reason": "NotFound"}), nil
		}
		return reply(200, j), nil
	case r.Method == http.MethodPatch:
		if r.Header.Get("Content-Type") != "application/merge-patch+json" {
			return reply(415, map[string]any{"reason": "UnsupportedMediaType"}), nil
		}
		j, ok := f.jobs[name]
		if !ok {
			return reply(404, map[string]any{"reason": "NotFound"}), nil
		}
		if want, ok := body["metadata"].(map[string]any)["resourceVersion"]; ok && want != j["metadata"].(map[string]any)["resourceVersion"] {
			return reply(409, map[string]any{"reason": "Conflict"}), nil
		}
		merge(j, body)
		f.rv++
		j["metadata"].(map[string]any)["resourceVersion"] = strconv.Itoa(f.rv)
		return reply(200, j), nil
	}
	return reply(405, map[string]any{"reason": "MethodNotAllowed"}), nil
}

func merge(dst, patch map[string]any) {
	for k, v := range patch {
		if pm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				merge(dm, pm)
				continue
			}
		}
		dst[k] = v
	}
}

func tokenFile(t *testing.T, token string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(p, []byte(token+"\n"), 0o600))
	return p
}

func testKube(t *testing.T, api *fakeAPI, token string) (*Kube, string) {
	t.Helper()
	tf := tokenFile(t, token)
	k, err := newKubeWith(testServer, "prow", tf, &http.Client{Transport: api})
	require.NoError(t, err)
	return k, tf
}

func TestKubeCreateGetListPatch(t *testing.T) {
	api := newFakeAPI("tok-1")
	k, tf := testKube(t, api, "tok-1")
	ctx := context.Background()
	pj, err := testCatalog(t).Build(testConfig, goldenAddField)
	require.NoError(t, err)

	require.NoError(t, k.Create(ctx, pj))
	assert.ErrorIs(t, k.Create(ctx, pj), ErrExists)
	got, err := k.Get(ctx, pj.Metadata.Name)
	require.NoError(t, err)
	assert.Equal(t, "1", got.Metadata.ResourceVersion)
	assert.Equal(t, "triggered", got.Status.State)
	_, err = k.Get(ctx, "autopilot-ffffffffffffffff-g1-r1")
	assert.ErrorIs(t, err, ErrNotFound)

	list, err := k.List(ctx, LabelManagedBy+"="+ManagedBy)
	require.NoError(t, err)
	assert.Len(t, list, 1)

	// The token is read again on every request (bound tokens rotate).
	api.token = "tok-2"
	require.NoError(t, os.WriteFile(tf, []byte("tok-2"), 0o600))
	assert.ErrorIs(t, k.Patch(ctx, pj.Metadata.Name, []byte(`{"metadata":{"resourceVersion":"9"}}`)), ErrConflict)
	require.NoError(t, k.Patch(ctx, pj.Metadata.Name, []byte(`{"metadata":{"resourceVersion":"1"},"status":{"state":"aborted"}}`)))
	got, err = k.Get(ctx, pj.Metadata.Name)
	require.NoError(t, err)
	assert.Equal(t, "aborted", got.Status.State)

	assert.Contains(t, api.requests, "POST /apis/prow.k8s.io/v1/namespaces/prow/prowjobs")
	assert.Contains(t, api.requests, "GET /apis/prow.k8s.io/v1/namespaces/prow/prowjobs?labelSelector=ack-autopilot%2Fmanaged-by%3Dautopilot-dispatcher&limit=100")
}

func TestKubeErrorsNeverHoldTheToken(t *testing.T) {
	api := newFakeAPI("secret-token")
	api.status, api.body = 403, `{"reason":"Forbidden","message":"prowjobs is forbidden: User \"system:serviceaccount:test-pods:autopilot-dispatcher\" cannot delete"}`
	k, _ := testKube(t, api, "secret-token")
	_, err := k.Get(context.Background(), "autopilot-0123456789abcdef-g1-r1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403 Forbidden")
	assert.NotContains(t, err.Error(), "secret-token")

	api.status, api.body = 200, strings.Repeat("x", maxKubeBody+1)
	_, err = k.Get(context.Background(), "autopilot-0123456789abcdef-g1-r1")
	assert.ErrorContains(t, err, "larger than")
}

func TestKubeRefuses(t *testing.T) {
	api := newFakeAPI("t")
	k, _ := testKube(t, api, "t")
	_, err := k.Get(context.Background(), "../../secrets/x")
	assert.ErrorContains(t, err, "not a ProwJob name")
	assert.Empty(t, api.requests, "nothing was sent")

	_, err = newKubeWith("http://10.0.0.1", "prow", "t", http.DefaultClient)
	assert.ErrorContains(t, err, "https")
	_, err = newKubeWith(testServer, "Prow", "t", http.DefaultClient)
	assert.ErrorContains(t, err, "namespace")
	_, err = newKube(testServer, "prow", "t", []byte("not a pem"))
	assert.ErrorContains(t, err, "no CA certificate")
	_, err = InCluster("prow", func(string) string { return "" })
	assert.ErrorContains(t, err, "not in a cluster")

	empty, _ := testKube(t, api, "")
	_, err = empty.Get(context.Background(), "autopilot-0123456789abcdef-g1-r1")
	assert.ErrorContains(t, err, "token is empty")
}
