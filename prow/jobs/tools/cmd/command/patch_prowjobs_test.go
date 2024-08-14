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
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/ecrpublic/types"
	"github.com/stretchr/testify/assert"
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
			imageTags := cleanECRConfigVersionList(tt.args.versions)
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
			versions := getECRConfigVersionList(tt.args.imageDetails)
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
				configTagsMap: map[string]string{"tag": "tag-0.0.3"},
				ecrTagsMap:    map[string]string{"tag": "0.0.1"},
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
