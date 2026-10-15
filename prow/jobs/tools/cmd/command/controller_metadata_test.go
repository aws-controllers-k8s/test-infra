package command

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadGenerateMetadata(t *testing.T) {
	sdkVersion, serviceSDKVersion, err := readGenerateMetadata(fakeControllerPath)
	require.NoError(t, err)
	assert.Equal(t, "v1.41.5", sdkVersion)
	assert.Equal(t, "v1.95.1", serviceSDKVersion)
}

func TestReadGenerateMetadataMissing(t *testing.T) {
	_, _, err := readGenerateMetadata("../../../testdata/does-not-exist")
	assert.Error(t, err)
}

// writeMetadataFixture builds a throwaway controller directory containing only
// an ack-generate-metadata.yaml with the given body.
func writeMetadataFixture(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "apis", "v1alpha1")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "ack-generate-metadata.yaml"), []byte(body), 0o644,
	))
	return root
}

func TestReadGenerateMetadataServiceVersionOnly(t *testing.T) {
	// Per-service-pinned controllers omit aws_sdk_go_version; it must not be required.
	root := writeMetadataFixture(t, `api_version: v1alpha1
aws_service_sdk_version: v1.95.1
`)

	sdkVersion, serviceSDKVersion, err := readGenerateMetadata(root)
	require.NoError(t, err)
	assert.Equal(t, "", sdkVersion, "core pin is legitimately absent here")
	assert.Equal(t, "v1.95.1", serviceSDKVersion)
}

func TestReadGenerateMetadataCoreVersionOnly(t *testing.T) {
	// The common shape: a core pin and no per-service pin.
	root := writeMetadataFixture(t, `api_version: v1alpha1
aws_sdk_go_version: v1.41.5
`)

	sdkVersion, serviceSDKVersion, err := readGenerateMetadata(root)
	require.NoError(t, err)
	assert.Equal(t, "v1.41.5", sdkVersion)
	assert.Equal(t, "", serviceSDKVersion)
}

func TestReadGenerateMetadataNeitherVersion(t *testing.T) {
	root := writeMetadataFixture(t, `api_version: v1alpha1
`)

	_, _, err := readGenerateMetadata(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aws_sdk_go_version")
	assert.Contains(t, err.Error(), "aws_service_sdk_version")
}
