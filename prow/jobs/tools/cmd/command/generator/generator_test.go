package generator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateJobsConfigOK(t *testing.T) {
	cfg := &JobsConfig{
		AWSServices:                  []string{"s3", "ecr"},
		APINotificationServices:      []string{"s3"},
		APINotificationMaxOpenIssues: 1,
	}
	require.NoError(t, validateJobsConfig(cfg))
}

func TestValidateJobsConfigEmptyListNeedsNoCap(t *testing.T) {
	cfg := &JobsConfig{
		AWSServices: []string{"s3"},
	}
	require.NoError(t, validateJobsConfig(cfg))
}

func TestValidateJobsConfigRejectsNonSubsetService(t *testing.T) {
	// The cap satisfies both cap rules (positive, and no larger than the notified
	// list), so the subset rule is the only one this fixture breaks. It used to pair
	// a one-entry aws_services with a cap of 10, which the cap rule rejects too, and
	// passed only because the subset check runs first.
	cfg := &JobsConfig{
		AWSServices:                  []string{"ecr", "iam", "lambda"},
		APINotificationServices:      []string{"s3"},
		APINotificationMaxOpenIssues: 1,
	}
	err := validateJobsConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "s3")
	assert.Contains(t, err.Error(), "aws_services")
}

func TestValidateJobsConfigReportsTheSubsetErrorFirst(t *testing.T) {
	// A config that breaks both rules at once must name the service, not the cap:
	// the service is the actionable half, and a cap that looks wrong only because
	// the list is wrong would send the reader to the wrong line.
	cfg := &JobsConfig{
		AWSServices:                  []string{"s3", "ecr", "iam"},
		APINotificationServices:      []string{"bogussvc"},
		APINotificationMaxOpenIssues: 99,
	}
	err := validateJobsConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogussvc")
	assert.NotContains(t, err.Error(), "api_notification_max_open_issues")
}

func TestValidateJobsConfigRejectsMissingCap(t *testing.T) {
	cfg := &JobsConfig{
		AWSServices:             []string{"s3"},
		APINotificationServices: []string{"s3"},
	}
	err := validateJobsConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api_notification_max_open_issues")
}

func TestValidateJobsConfigRejectsDuplicateService(t *testing.T) {
	// Duplicates used to validate cleanly, and the caller resolves each service's
	// existing issue from a single listAPIChangeIssues call for the whole run, so
	// the repeat would still see no existing issue and file a second one.
	cfg := &JobsConfig{
		AWSServices:                  []string{"s3", "ecr", "iam"},
		APINotificationServices:      []string{"s3", "s3"},
		APINotificationMaxOpenIssues: 2,
	}
	err := validateJobsConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "more than once")
	assert.Contains(t, err.Error(), "s3")
}

func TestValidateJobsConfigRejectsCapThatCannotBind(t *testing.T) {
	// The run files at most one issue per *notified* service, so a cap above the
	// length of api_notification_services is a typo rather than a policy — however
	// many entries aws_services happens to hold. Measured against aws_services
	// instead, a cap of 2 here would be accepted despite there being one service.
	for _, maxOpen := range []int{2, 3, 74, 1000000} {
		cfg := &JobsConfig{
			AWSServices:                  []string{"s3", "ecr", "iam"},
			APINotificationServices:      []string{"s3"},
			APINotificationMaxOpenIssues: maxOpen,
		}
		err := validateJobsConfig(cfg)
		require.Error(t, err, "maxOpen=%d", maxOpen)
		assert.Contains(t, err.Error(), "cannot bind")
	}
}

func TestValidateJobsConfigAcceptsACapEqualToTheNotifiedListLength(t *testing.T) {
	// One issue per notified service is the honest policy, and bounding the cap by
	// len(aws_services) rejected it — which also left a single-service config
	// unrepresentable, its cap having to be both greater than 0 and less than 1.
	// Equality is allowed because the open count includes stale issues for services
	// since removed from the list, so a cap equal to the list length can still bind.
	for _, cfg := range []*JobsConfig{
		{
			AWSServices:                  []string{"s3"},
			APINotificationServices:      []string{"s3"},
			APINotificationMaxOpenIssues: 1,
		},
		{
			AWSServices:                  []string{"s3", "ecr", "iam"},
			APINotificationServices:      []string{"s3", "ecr", "iam"},
			APINotificationMaxOpenIssues: 3,
		},
	} {
		require.NoError(t, validateJobsConfig(cfg))
	}
}

func TestValidateJobsConfigRejectsNilConfig(t *testing.T) {
	err := validateJobsConfig(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsed to nothing")
}

func TestValidateAPINotificationServicesDirectly(t *testing.T) {
	// Called the way Task 15's reader calls it: the two lists on their own, with no
	// JobsConfig and no cap, after unmarshalling and before any service is processed.
	awsServices := []string{"s3", "ecr", "iam"}

	tests := []struct {
		name     string
		services []string
		wantErr  string
	}{
		{name: "empty list", services: nil},
		{name: "single subset entry", services: []string{"s3"}},
		{name: "all entries", services: []string{"s3", "ecr", "iam"}},
		{name: "not in aws_services", services: []string{"bogussvc"}, wantErr: "bogussvc"},
		{name: "duplicate", services: []string{"s3", "s3"}, wantErr: "more than once"},
		// The subset rule covers these three without a check of its own.
		{name: "empty entry", services: []string{""}, wantErr: "is not in aws_services"},
		{name: "case mismatch", services: []string{"S3"}, wantErr: "is not in aws_services"},
		{name: "untrimmed whitespace", services: []string{" s3 "}, wantErr: "is not in aws_services"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAPINotificationServices(tt.services, awsServices)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestLoadConfigReadsAndValidatesAPINotificationFields(t *testing.T) {
	// Pins the yaml tags against the real file: a typo in either tag would silently
	// produce a zero value here, while every struct-literal test above — which sets
	// the fields directly — would still pass.
	//
	// It does *not* pin the wiring. loadConfig's call to validateJobsConfig is held in
	// place by TestLoadConfigRejectsAnInvalidConfig and
	// TestLoadConfigRejectsAnEmptyConfig, which are the two that fail if it is
	// deleted; this one reads a config that validates, so it passes either way.
	config, err := loadConfig("../../../../jobs_config.yaml")
	require.NoError(t, err)
	assert.Equal(t, []string{"s3"}, config.APINotificationServices)
	assert.Equal(t, 1, config.APINotificationMaxOpenIssues)
	assert.Contains(t, config.AWSServices, "s3")
}

func TestLoadConfigRejectsAnInvalidConfig(t *testing.T) {
	// The wiring, from the other side: loadConfig must refuse a config
	// validateJobsConfig rejects, not merely parse it. The cap satisfies both cap
	// rules so the subset rule is the only one broken; a one-entry aws_services with
	// this cap used to break the cap rule too.
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs_config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"aws_services:\n  - s3\n  - ecr\n  - iam\napi_notification_services:\n  - bogussvc\n"+
			"api_notification_max_open_issues: 1\n"), 0o644))

	_, err := loadConfig(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogussvc")
}

func TestLoadConfigRejectsAnEmptyConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs_config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("# nothing\n"), 0o644))

	_, err := loadConfig(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsed to nothing")
}
