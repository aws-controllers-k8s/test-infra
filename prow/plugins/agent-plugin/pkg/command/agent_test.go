//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package command

import "testing"

func TestValidateArgs(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		required []string
		optional []string
		wantErr  string
	}{
		{
			name:     "declared required and optional arguments",
			command:  "/agent add-field service=s3 resource=Bucket field=BucketKeyEnabled model=opus",
			required: []string{"service", "resource", "field"},
			optional: []string{"model", "aws-sdk-version"},
		},
		{
			name:     "field rejected by add-resource",
			command:  "/agent add-resource service=s3 resource=Bucket field=BucketKeyEnabled",
			required: []string{"service", "resource"},
			optional: []string{"model", "aws-sdk-version"},
			wantErr:  "unsupported arguments for workflow add-resource: field",
		},
		{
			name:     "unsupported arguments sorted",
			command:  "/agent add-resource service=s3 resource=Bucket zeta=1 alpha=2",
			required: []string{"service", "resource"},
			wantErr:  "unsupported arguments for workflow add-resource: alpha, zeta",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := ParseAgentCommand(tt.command)
			if err != nil {
				t.Fatalf("ParseAgentCommand: %v", err)
			}
			err = cmd.ValidateArgs(tt.required, tt.optional)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateArgs: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("ValidateArgs error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
