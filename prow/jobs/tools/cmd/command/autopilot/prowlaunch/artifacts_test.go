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
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

var testCreds = aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
	return aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "not-a-secret"}, nil
})

const testKey = "logs/agent-workflow-add-field/1234/artifacts/agent-result.json"

func s3With(t *testing.T, code int, body string, seen *[]*http.Request) *S3Artifacts {
	t.Helper()
	s, err := newS3Artifacts("s3://example-prow-logs", "us-west-2", testCreds, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		*seen = append(*seen, r)
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})})
	require.NoError(t, err)
	s.now = func() time.Time { return time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC) }
	return s
}

func TestS3GetSignsAndLimits(t *testing.T) {
	var seen []*http.Request
	got, err := s3With(t, 200, `{"x":1}`, &seen).Get(context.Background(), testKey, 64)
	require.NoError(t, err)
	assert.Equal(t, `{"x":1}`, string(got))
	require.Len(t, seen, 1)
	r := seen[0]
	assert.Equal(t, "GET", r.Method)
	assert.Equal(t, "https://example-prow-logs.s3.us-west-2.amazonaws.com/"+testKey, r.URL.String())
	assert.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261117/us-west-2/s3/aws4_request"), r.Header.Get("Authorization"))
	assert.Equal(t, emptySHA256, r.Header.Get("X-Amz-Content-Sha256"))

	_, err = s3With(t, 200, strings.Repeat("x", 65), &seen).Get(context.Background(), testKey, 64)
	assert.True(t, errors.Is(err, ErrTooLarge), err)
	for _, code := range []int{403, 404} {
		_, err = s3With(t, code, "", &seen).Get(context.Background(), testKey, 64)
		assert.True(t, errors.Is(err, ErrNotFound), code)
	}
	_, err = s3With(t, 500, "", &seen).Get(context.Background(), testKey, 64)
	assert.ErrorContains(t, err, "HTTP 500")
}

func TestS3ReadsOnlyResultKeys(t *testing.T) {
	var seen []*http.Request
	s := s3With(t, 200, "", &seen)
	for _, key := range []string{
		"logs/agent-workflow-add-field/1234/artifacts/../../secret",
		"logs/agent-workflow-add-field/1234/finished.json",
		"pr-logs/x/1/artifacts/agent-result.json",
	} {
		_, err := s.Get(context.Background(), key, 64)
		assert.ErrorContains(t, err, "refusing", key)
	}
	assert.Empty(t, seen)
	_, err := newS3Artifacts("s3://a.b.c", "us-west-2", testCreds, http.DefaultClient)
	assert.Error(t, err)
	_, err = newS3Artifacts("s3://example-prow-logs", "evil.example.com/", testCreds, http.DefaultClient)
	assert.Error(t, err)
}
