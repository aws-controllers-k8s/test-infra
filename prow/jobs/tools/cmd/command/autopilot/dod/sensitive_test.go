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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const smithyFixture = `{"smithy":"2.0","shapes":{
 "com.amazonaws.x#Secret": {"type":"string","traits":{"smithy.api#sensitive":{}}},
 "com.amazonaws.x#Secrets": {"type":"list","member":{"target":"com.amazonaws.x#Secret"}},
 "com.amazonaws.x#SecretMap": {"type":"map","key":{"target":"smithy.api#String"},"value":{"target":"com.amazonaws.x#Secret"}},
 "com.amazonaws.x#CreateThingRequest": {"type":"structure","members":{
   "KmsKeyId": {"target":"com.amazonaws.x#Secret"},
   "Seeds": {"target":"com.amazonaws.x#Secrets"},
   "Env": {"target":"com.amazonaws.x#SecretMap"},
   "Note": {"target":"smithy.api#String","traits":{"smithy.api#sensitive":{}}},
   "Name": {"target":"smithy.api#String"}}},
 "com.amazonaws.x#Choice": {"type":"union","members":{"Pin": {"target":"com.amazonaws.x#Secret"}}}
}}`

func TestSmithySensitiveNames(t *testing.T) {
	names, err := smithySensitiveNames([]byte(smithyFixture))
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"kmskeyid": true, "seeds": true, "env": true, "note": true, "pin": true}, names)
	_, err = smithySensitiveNames([]byte(`{"shapes":{}}`))
	assert.Error(t, err)
}

const crdFixture = `---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
spec:
  versions:
  - name: v1alpha1
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
            properties:
              name: {type: string}
              masterUserPassword: {type: string}
              config:
                type: object
                properties:
                  kmsKeyID: {type: string}
                  size: {type: integer}
              seeds:
                type: array
                items:
                  type: object
                  properties:
                    note: {type: string}
              tokenRef:
                type: object
                properties:
                  key: {type: string}
                  name: {type: string}
                  namespace: {type: string}
`

func TestCheckSensitive(t *testing.T) {
	names, err := smithySensitiveNames([]byte(smithyFixture))
	require.NoError(t, err)
	schemas, err := crdVersionSchemas([]byte(crdFixture))
	require.NoError(t, err)
	crds := map[string][]map[string]any{"c.yaml": schemas}
	added := func(field, path string) crdChange {
		return crdChange{File: "c.yaml", Kind: "Thing", Bucket: "added_optional_spec", Field: field, Path: path, ChangeType: "PROPERTY_ADDED"}
	}

	d := doc(added("spec.config", ".properties.spec.properties.config"),
		added("spec.seeds", ".properties.spec.properties.seeds"),
		added("spec.tokenRef", ".properties.spec.properties.tokenRef"))
	d.SensitiveCandidates = []crdCandidate{{File: "c.yaml", Kind: "Thing", Field: "spec.masterUserPassword"}}
	c := checkSensitive(d, crds, names, nil)
	assert.Equal(t, StatusFail, c.Status)
	assert.Equal(t, ClassHandWritten, c.Reclassify)
	got := map[string]string{}
	for _, f := range c.Findings {
		got[f.Detail] = f.Signature
	}
	assert.Equal(t, map[string]string{
		"field spec.masterUserPassword": "SENSITIVE_NAME",
		"field spec.config.kmsKeyID":    "SENSITIVE_SMITHY",
		"field spec.seeds":              "SENSITIVE_SMITHY",
		"field spec.seeds[].note":       "SENSITIVE_SMITHY",
	}, got, "tokenRef is a SecretKeyReference, so neither it nor its members count")

	assert.Equal(t, StatusPass, checkSensitive(doc(added("spec.name", ".properties.spec.properties.name")), crds, names, nil).Status)

	newCRD := doc(crdChange{File: "c.yaml", Kind: "Thing", Bucket: "crd_added", ChangeType: "CRD_ADDED"})
	assert.Contains(t, checkSensitive(newCRD, crds, names, nil).Reasons, "SENSITIVE_NAME", "a new CRD is walked whole")
}
