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

package sdkbump

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
)

func TestReadPins(t *testing.T) {
	ctx := context.Background()
	r := newRepos(t)
	g := gitOps{dir: r.ctrl.Dir, run: dod.ExecRunner}
	p, err := readPins(ctx, g, "HEAD", "s3")
	require.NoError(t, err)
	assert.Equal(t, Pins{
		APIVersion: "v1alpha1", MetadataPath: metaPath,
		CoreSDK: "v1.41.5", CodegenVersion: "v0.64.0", CodegenHash: r.cgSHA,
		RuntimeVersion: "v0.68.0", GoModService: "v1.90.0", GoModCore: "v1.41.5", GoModSmithy: "v1.24.2",
	}, p)
}

func TestDecide(t *testing.T) {
	tg := Target{Package: "s3", Version: "v1.97.3"}
	ok := Pins{CoreSDK: "v1.41.5", CodegenVersion: "v0.64.0", CodegenHash: sha, RuntimeVersion: "v0.68.0", GoModService: "v1.90.0"}
	with := func(f func(*Pins)) Pins { p := ok; f(&p); return p }
	for name, c := range map[string]struct {
		p               Pins
		outcome, reason string
	}{
		"bump from core":     {ok, "", ""},
		"bump per-service":   {with(func(p *Pins) { p.CoreSDK, p.ServiceSDK = "", "v1.95.1" }), "", ""},
		"at target":          {with(func(p *Pins) { p.ServiceSDK = "v1.97.3" }), OutcomeNoChange, "ALREADY_AT_TARGET"},
		"past target":        {with(func(p *Pins) { p.ServiceSDK = "v1.98.0" }), OutcomeNoChange, "ALREADY_AT_TARGET"},
		"go.mod past target": {with(func(p *Pins) { p.GoModService = "v1.98.0" }), OutcomeNeedsHuman, "SDK_PIN_WOULD_DECREASE"},
		"v1 SDK":             {with(func(p *Pins) { p.LegacyV1 = true }), OutcomeNeedsHuman, "UNSUPPORTED_PIN_REGIME"},
		"no pin":             {with(func(p *Pins) { p.CoreSDK = "" }), OutcomeNeedsHuman, "UNSUPPORTED_PIN_REGIME"},
		"unreadable pin":     {with(func(p *Pins) { p.ServiceSDK = "1.97" }), OutcomeNeedsHuman, "SDK_PIN_UNREADABLE"},
		"dirty codegen":      {with(func(p *Pins) { p.CodegenVersion = "v0.64.0-dirty" }), OutcomeNeedsHuman, "CODEGEN_PIN_UNUSABLE"},
		"short build hash":   {with(func(p *Pins) { p.CodegenHash = "66fb531" }), OutcomeNeedsHuman, "CODEGEN_PIN_UNUSABLE"},
		"no runtime":         {with(func(p *Pins) { p.RuntimeVersion = "" }), OutcomeNeedsHuman, "RUNTIME_PIN_UNUSABLE"},
	} {
		o, why := decide(c.p, tg)
		assert.Equal(t, [2]string{c.outcome, c.reason}, [2]string{o, why}, name)
	}
}

func TestRuntimeRev(t *testing.T) {
	rev, err := runtimeRev("v0.68.0")
	require.NoError(t, err)
	assert.Equal(t, "v0.68.0", rev)
	rev, err = runtimeRev("v0.68.1-0.20261110120000-0123456789ab")
	require.NoError(t, err)
	assert.Equal(t, "0123456789ab", rev)
	_, err = runtimeRev("main")
	assert.Error(t, err)
}

func TestCodegenSupportsServicePin(t *testing.T) {
	r := newRepos(t)
	assert.True(t, codegenSupportsServicePin(r.codegen.Dir))
	assert.False(t, codegenSupportsServicePin(t.TempDir()))
}
