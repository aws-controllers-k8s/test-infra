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

package dod

import (
	"os"
	"strings"
	"testing"

	awssdkmodel "github.com/aws-controllers-k8s/code-generator/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func widgetsModel(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/widgets.json")
	require.NoError(t, err)
	return b
}

func widgetsAPI(t *testing.T) *awssdkmodel.API {
	t.Helper()
	api, err := loadAPI(widgetsModel(t), "widgets")
	require.NoError(t, err)
	return api
}

// The generator.yaml namespace is ACK's: operation inputs are <Op>Input.
func TestLoadAPIUsesACKShapeNames(t *testing.T) {
	api := widgetsAPI(t)
	assert.Contains(t, api.Shapes, "CreateWidgetInput")
	assert.Contains(t, api.Operations, "DeleteGizmo")
}

func TestCheckIgnore(t *testing.T) {
	spec := ignoreSpec{
		FieldPaths: []string{
			"CreateWidgetInput.Name", "CreateWidgetInput.Config.Size",
			"CreateWidgetInput.name", "CreateWidgetInput.Config.Labels.Key", "CreateWidgetInput.Nope",
			"NoSuchInput.Name", "Lonely",
		},
		ShapeNames:    []string{"WidgetConfig", "NoShape"},
		Operations:    []string{"DeleteGizmo", "DeleteNothing"},
		ResourceNames: []string{"Gizmo", "Sprocket"},
	}
	c := checkIgnore(spec, widgetsAPI(t))
	got := map[string]string{}
	for _, f := range c.Findings {
		got[strings.SplitN(f.Detail, ":", 2)[0]] = f.Signature
	}
	assert.Equal(t, map[string]string{
		"CreateWidgetInput.name":              "IGNORE_FIELD_PATH_NO_EFFECT", // codegen deletes by exact name
		"CreateWidgetInput.Config.Labels.Key": "IGNORE_FIELD_PATH_NO_EFFECT", // parent is a list
		"CreateWidgetInput.Nope":              "IGNORE_FIELD_PATH_NO_EFFECT",
		"NoSuchInput.Name":                    "IGNORE_UNKNOWN_SHAPE",
		"Lonely":                              "IGNORE_FIELD_PATH_INVALID",
		"NoShape":                             "IGNORE_SHAPE_NO_EFFECT",
		"DeleteNothing":                       "IGNORE_OPERATION_UNKNOWN",
		"Sprocket":                            "IGNORE_RESOURCE_UNMATCHED",
	}, got)
	assert.Equal(t, StatusFail, c.Status)
}

func TestIgnoreCheckInputs(t *testing.T) {
	base := []byte("ignore:\n  field_paths:\n  - CreateWidgetInput.Type\n")
	same := ignoreCheck(base, base, nil, "widgets")
	assert.Equal(t, StatusPass, same.Status, "nothing added: no model needed")

	head := []byte("ignore:\n  field_paths:\n  - CreateWidgetInput.Type\n  - CreateWidgetInput.Name\n")
	assert.Equal(t, []string{"SDK_MODEL_MISSING"}, ignoreCheck(base, head, nil, "widgets").Reasons)
	assert.Equal(t, StatusPass, ignoreCheck(base, head, widgetsModel(t), "widgets").Status)
	assert.Equal(t, []string{"GENERATOR_CONFIG_UNREADABLE"}, ignoreCheck(base, []byte("ignore: [\n"), nil, "widgets").Reasons)
}
