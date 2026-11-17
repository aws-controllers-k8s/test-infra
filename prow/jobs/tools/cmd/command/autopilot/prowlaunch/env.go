package prowlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Env is what the launcher takes from the Dispatcher job's environment.
type Env struct {
	Vars   Vars
	Bucket string // from JOB_SPEC
	Region string // AWS_REGION
}

// EnvFrom reads the environment of a decorated Prow pod: PROW_IMAGES_REPO_URI
// and TEST_INFRA_ORG (set by the autopilot-dispatch periodic and
// preset-github-secrets), AWS_REGION (Prow's default decoration env), and
// JOB_SPEC, whose decoration_config names the logs bucket this Prow uploads
// to. The Dispatcher's runs upload to the same bucket.
func EnvFrom(getenv func(string) string) (Env, error) {
	e := Env{
		Vars:   Vars{ImagesRepo: getenv("PROW_IMAGES_REPO_URI"), TestInfraOrg: getenv("TEST_INFRA_ORG")},
		Region: getenv("AWS_REGION"),
	}
	if err := e.Vars.validate(); err != nil {
		return Env{}, err
	}
	var spec struct {
		DecorationConfig *struct {
			GCSConfiguration *struct {
				Bucket string `json:"bucket"`
			} `json:"gcs_configuration"`
		} `json:"decoration_config"`
	}
	raw := getenv("JOB_SPEC")
	if raw == "" {
		return Env{}, errors.New("JOB_SPEC is unset: not a decorated Prow job")
	}
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return Env{}, fmt.Errorf("JOB_SPEC: %w", err)
	}
	if spec.DecorationConfig == nil || spec.DecorationConfig.GCSConfiguration == nil {
		return Env{}, errors.New("JOB_SPEC has no decoration_config.gcs_configuration")
	}
	e.Bucket = spec.DecorationConfig.GCSConfiguration.Bucket
	if !bucketURLRE.MatchString(e.Bucket) {
		return Env{}, fmt.Errorf("JOB_SPEC bucket %q is not s3://<bucket>", e.Bucket)
	}
	if !regionRE.MatchString(e.Region) {
		return Env{}, fmt.Errorf("AWS_REGION %q is not a region", e.Region)
	}
	return e, nil
}

// NewInCluster is the launcher a Dispatcher job uses: the catalog from its
// test-infra checkout, the cluster's API with the pod's ServiceAccount, and
// the logs bucket with the pod's AWS credentials.
func NewInCluster(ctx context.Context, testInfraDir string, getenv func(string) string) (*Launcher, error) {
	e, err := EnvFrom(getenv)
	if err != nil {
		return nil, err
	}
	cat, err := FromCheckout(testInfraDir, e.Vars)
	if err != nil {
		return nil, err
	}
	kube, err := InCluster(Namespace, getenv)
	if err != nil {
		return nil, err
	}
	art, err := NewS3Artifacts(ctx, e.Bucket, e.Region)
	if err != nil {
		return nil, err
	}
	return New(cat, Config{Bucket: e.Bucket, Org: e.Vars.TestInfraOrg, Now: time.Now}, kube, art)
}
