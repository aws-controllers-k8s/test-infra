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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/ecrpublic/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/generator"
)

func TestCleanECRConfigVersionList(t *testing.T) {
	assert := assert.New(t)

	type args struct {
		versions []string
	}

	tests := []struct {
		name             string
		args             args
		wantLenImageTags int
	}{
		{
			name:             "empty versions",
			args:             args{versions: []string{}},
			wantLenImageTags: 0,
		},
		{
			name:             "malformed versions - no prefix",
			args:             args{versions: []string{"tagName-0.0.1", "v0.0.2"}},
			wantLenImageTags: 1,
		},
		{
			name:             "correct versions",
			args:             args{versions: []string{"tagName-0.0.1", "another-tag-name-v0.0.2"}},
			wantLenImageTags: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imageTags := getHighestEcrImageVersionMap(tt.args.versions)
			assert.Len(imageTags, tt.wantLenImageTags)
		})
	}
}

func TestGetECRConfigVersionList(t *testing.T) {
	assert := assert.New(t)

	type args struct {
		imageDetails []types.ImageDetail
	}

	tests := []struct {
		name            string
		args            args
		wantLenVersions int
	}{
		{
			name:            "Empty image details",
			args:            args{imageDetails: []types.ImageDetail{}},
			wantLenVersions: 0,
		},
		{
			name: "malformed image details",
			args: args{imageDetails: []types.ImageDetail{
				{ImageTags: []string{"v0.0.1", "someWord-v0.0.1"}},
				{ImageTags: []string{"v0.0.1", "someWord-v0.0.1"}},
			}},
			wantLenVersions: 0,
		},
		{
			name: "correct image details",
			args: args{imageDetails: []types.ImageDetail{
				{ImageTags: []string{"tag-0.0.1", "tag-v0.0.1"}},
				{ImageTags: []string{"another-tag-0.0.2", "another-tag-v0.0.2"}},
			}},
			wantLenVersions: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			versions := getEcrImageVersionList(tt.args.imageDetails)
			assert.Len(versions, tt.wantLenVersions)
		})
	}
}

func TestCompareImageVersions(t *testing.T) {
	assert := assert.New(t)

	type args struct {
		configTagsMap map[string]string
		ecrTagsMap    map[string]string
	}

	tests := []struct {
		name               string
		args               args
		wantLenTagsToBuild int
		wantErr            bool
	}{
		{
			name: "empty maps",
			args: args{
				configTagsMap: map[string]string{},
				ecrTagsMap:    map[string]string{},
			},
			wantLenTagsToBuild: 0,
			wantErr:            false,
		},
		{
			name: "empty ecrTagsMaps",
			args: args{
				configTagsMap: map[string]string{
					"tag":         "tag-0.0.1",
					"another-tag": "another-tag-0.0.2",
				},
				ecrTagsMap: map[string]string{},
			},
			wantLenTagsToBuild: 2,
			wantErr:            false,
		},
		{
			name: "malformed versions",
			args: args{
				configTagsMap: map[string]string{"tag": "tag-0.0.1"},
				ecrTagsMap:    map[string]string{"tag": "v0.0.1"},
			},
			wantLenTagsToBuild: 0,
			wantErr:            true,
		},
		{
			name: "correct versions",
			args: args{
				configTagsMap: map[string]string{"tag": "tag-0.0.3", "another-tag": "another-tag-0.0.3"},
				ecrTagsMap:    map[string]string{"tag": "0.0.1", "another-tag": "0.0.3"},
			},
			wantLenTagsToBuild: 1,
			wantErr:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tagsToBuild, err := compareImageVersions(tt.args.configTagsMap, tt.args.ecrTagsMap)
			if (err != nil) != tt.wantErr {
				assert.Fail(fmt.Sprintf("compareImageVersions() error = %v, wantErr %v", err, tt.wantErr))
			}
			assert.Len(tagsToBuild, tt.wantLenTagsToBuild)
		})
	}
}

// TestResolveGenerateConfigPath verifies that the generation-config path
// falls back to the build/push config path when unset (backward compatible),
// and otherwise routes to the explicitly provided raw-config path.
func TestResolveGenerateConfigPath(t *testing.T) {
	assert := assert.New(t)

	tests := []struct {
		name               string
		generateConfigPath string
		imagesConfigPath   string
		want               string
	}{
		{
			name:               "empty generate path falls back to images config path",
			generateConfigPath: "",
			imagesConfigPath:   "/tmp/images_config.yaml",
			want:               "/tmp/images_config.yaml",
		},
		{
			name:               "explicit generate path is used verbatim",
			generateConfigPath: "./prow/jobs/images_config.yaml",
			imagesConfigPath:   "/tmp/images_config.yaml",
			want:               "./prow/jobs/images_config.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveGenerateConfigPath(tt.generateConfigPath, tt.imagesConfigPath)
			assert.Equal(tt.want, got)
		})
	}
}

// TestGenerateManifestKeepsRepoVariable proves the core fix: when the generator
// is fed the RAW images_config.yaml (image_repo: ${PROW_IMAGES_REPO_URI}), the
// rendered manifest keeps the ${PROW_IMAGES_REPO_URI} variable rather than
// baking in a resolved ECR URI. This is what eliminates the ~1000-line churn
// diff between the bot output and `make prow-gen` output.
func TestGenerateManifestKeepsRepoVariable(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	dir := t.TempDir()

	// RAW config: image_repo is the unresolved variable.
	rawConfigPath := filepath.Join(dir, "images_config.yaml")
	rawConfig := "image_repo: ${PROW_IMAGES_REPO_URI}\n" +
		"images:\n" +
		"    kubectl: prow-kubectl-0.0.2\n"
	require.NoError(os.WriteFile(rawConfigPath, []byte(rawConfig), 0o644))

	// RESOLVED config: image_repo is the concrete ECR URI (as envsubst would
	// produce in the bot). Used to prove the OLD behavior would hardcode it.
	resolvedConfigPath := filepath.Join(dir, "images_config_resolved.yaml")
	resolvedConfig := "image_repo: public.ecr.aws/m5q3e4b2/ack-test-infra-prod-prow-images\n" +
		"images:\n" +
		"    kubectl: prow-kubectl-0.0.2\n"
	require.NoError(os.WriteFile(resolvedConfigPath, []byte(resolvedConfig), 0o644))

	// A minimal template that renders "<image_repo>:<tag>", exactly the shape
	// the real job-config-job.yaml.tpl uses for the kubectl image.
	templatePath := filepath.Join(dir, "manifest.tpl")
	template := `image: {{printf "%s:%s" .ImageContext.ImageRepo (index .ImageContext.Images "kubectl") }}` + "\n"
	require.NoError(os.WriteFile(templatePath, []byte(template), 0o644))

	// Generation with RAW config must retain the variable form.
	rawOut := filepath.Join(dir, "raw_out.yaml")
	require.NoError(generator.GenerateManifest(rawConfigPath, templatePath, rawOut))
	rawBytes, err := os.ReadFile(rawOut)
	require.NoError(err)
	rawStr := string(rawBytes)
	assert.Contains(rawStr, "${PROW_IMAGES_REPO_URI}:prow-kubectl-0.0.2",
		"raw-config generation must keep the ${PROW_IMAGES_REPO_URI} variable")
	assert.NotContains(rawStr, "public.ecr.aws/m5q3e4b2",
		"raw-config generation must NOT hardcode a resolved ECR URI")

	// Sanity: with the resolved config the manifest hardcodes the URI. This is
	// the churn the fix avoids by routing generation through the raw config.
	resolvedOut := filepath.Join(dir, "resolved_out.yaml")
	require.NoError(generator.GenerateManifest(resolvedConfigPath, templatePath, resolvedOut))
	resolvedBytes, err := os.ReadFile(resolvedOut)
	require.NoError(err)
	assert.Contains(string(resolvedBytes), "public.ecr.aws/m5q3e4b2",
		"resolved-config generation hardcodes the URI (the old churn-causing behavior)")
}
