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

package config

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFindHighestTagVersion(t *testing.T) {
	assert := assert.New(t)

	type args struct {
		tags []string
	}

	tests := []struct {
		name        string
		args        args
		wantVersion string
		wantErr     bool
	}{
		{
			name:        "empty tags",
			args:        args{[]string{}},
			wantVersion: "",
			wantErr:     true,
		},
		{
			name:        "one tag",
			args:        args{[]string{"1.23.0"}},
			wantVersion: "1.23.0",
			wantErr:     false,
		},
		{
			name:        "malformed tag",
			args:        args{[]string{"1.23.0", "1./3.3"}},
			wantVersion: "",
			wantErr:     true,
		},
		{
			name:        "correct versions",
			args:        args{[]string{"1.0.0", "1.1.1", "2.2.2", "0.10.1", "12.34.91", "12.34.92"}},
			wantVersion: "12.34.92",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version, err := FindHighestTagVersion(tt.args.tags)
			if (err != nil) != tt.wantErr {
				assert.Fail(fmt.Sprintf("FindHighestTagVersion() error = %v, wantErr %v", err, tt.wantErr))
			}
			assert.Equal(tt.wantVersion, version)
		})
	}
}
