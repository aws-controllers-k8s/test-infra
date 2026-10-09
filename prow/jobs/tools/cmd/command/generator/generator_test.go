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
	// The cap is valid, so the subset rule is the only one this fixture breaks.
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
	// When both rules break, the error names the service, which is the actionable half.
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
	// Duplicates would each see no existing issue and file a second one.
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
	// The cap is bounded by api_notification_services, not aws_services.
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
	// A cap equal to the list length is allowed: stale issues for removed services still
	// count toward the open total, so it can still bind.
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
	// Called with just the two lists, before any JobsConfig or cap exists.
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
	// Pins the yaml tags against the real file; struct-literal tests would miss a tag typo.
	// The loadConfig wiring is covered by TestLoadConfigRejects*.
	config, err := loadConfig("../../../../jobs_config.yaml")
	require.NoError(t, err)
	assert.Equal(t, []string{"s3"}, config.APINotificationServices)
	assert.Equal(t, 1, config.APINotificationMaxOpenIssues)
	assert.Contains(t, config.AWSServices, "s3")
}

func TestLoadConfigRejectsAnInvalidConfig(t *testing.T) {
	// loadConfig must reject a config that validateJobsConfig rejects. Only the subset rule
	// is broken here.
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
