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
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
)

// ErrTooLarge: the object is larger than the caller allows.
var ErrTooLarge = errors.New("object too large")

// Artifacts reads one object from the Prow logs bucket.
type Artifacts interface {
	// Get returns the object, at most max bytes. ErrNotFound if it does not
	// exist; S3 answers 403 for a missing key to a reader that may not list
	// the bucket, so 403 counts as not found. ErrTooLarge past max.
	Get(ctx context.Context, key string, max int64) ([]byte, error)
}

// emptySHA256 is the payload hash of a GET.
const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

var (
	bucketRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
	regionRE = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)
	// resultKeyRE is the only kind of key the Dispatcher reads: one run's
	// agent-result.json, verdict.json or verdict.sig (Prow's path_strategy
	// explicit, periodic layout).
	resultKeyRE = regexp.MustCompile(`^logs/[a-z0-9-]{1,63}/[0-9]{1,20}/artifacts/((agent-result|verdict)\.json|verdict\.sig)$`)
)

// S3Artifacts reads the bucket with SigV4-signed GETs, using the pod's AWS
// credentials (EKS Pod Identity). It uses only the core SDK module this repo
// already requires; the S3 client module is not needed for one GET.
type S3Artifacts struct {
	bucket, region string
	creds          aws.CredentialsProvider
	signer         *v4.Signer
	client         *http.Client
	now            func() time.Time
}

// NewS3Artifacts reads bucketURL ("s3://<bucket>") in region.
func NewS3Artifacts(ctx context.Context, bucketURL, region string) (*S3Artifacts, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, err
	}
	return newS3Artifacts(bucketURL, region, cfg.Credentials, &http.Client{
		Transport: &http.Transport{Proxy: nil, TLSHandshakeTimeout: 10 * time.Second},
		Timeout:   30 * time.Second,
	})
}

func newS3Artifacts(bucketURL, region string, creds aws.CredentialsProvider, client *http.Client) (*S3Artifacts, error) {
	bucket, ok := strings.CutPrefix(bucketURL, "s3://")
	if !ok || !bucketRE.MatchString(bucket) {
		return nil, fmt.Errorf("logs bucket %q is not s3://<bucket> (no dots)", bucketURL)
	}
	if !regionRE.MatchString(region) {
		return nil, fmt.Errorf("AWS region %q is not a region", region)
	}
	return &S3Artifacts{bucket: bucket, region: region, creds: creds, signer: v4.NewSigner(), client: client, now: time.Now}, nil
}

func (s *S3Artifacts) Get(ctx context.Context, key string, max int64) ([]byte, error) {
	if !resultKeyRE.MatchString(key) {
		return nil, fmt.Errorf("refusing to read %q: not a run's agent-result.json, verdict.json or verdict.sig", key)
	}
	u := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", s.bucket, s.region, key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Amz-Content-Sha256", emptySHA256)
	creds, err := s.creds.Retrieve(ctx)
	if err != nil {
		return nil, fmt.Errorf("AWS credentials: %w", err)
	}
	if err := s.signer.SignHTTP(ctx, creds, req, emptySHA256, "s3", s.region, s.now()); err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusForbidden:
		return nil, fmt.Errorf("get %s: %w", key, ErrNotFound)
	default:
		return nil, fmt.Errorf("get %s: HTTP %d", key, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("get %s: %w (more than %d bytes)", key, ErrTooLarge, max)
	}
	return data, nil
}
