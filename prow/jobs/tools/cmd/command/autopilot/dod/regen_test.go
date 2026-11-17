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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod/dodtest"
)

const genSDK = dodtest.GoHeader + "package widget\n"

func metaYAML(codegen, buildDate string) string {
	return "ack_generate_info:\n  build_date: \"" + buildDate + "\"\n  version: " + codegen + "\naws_sdk_go_version: v1.41.5\n"
}

// fakeMake "regenerates" by writing files under SERVICE_CONTROLLER_SOURCE_PATH.
func fakeMake(files map[string]string, fail bool) Runner {
	return func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, []byte, error) {
		if name != "make" {
			return nil, nil, fmt.Errorf("unexpected command %s", name)
		}
		if fail {
			return nil, []byte("boom"), errors.New("exit status 2")
		}
		root := ""
		for _, e := range env {
			if v, ok := strings.CutPrefix(e, "SERVICE_CONTROLLER_SOURCE_PATH="); ok {
				root = v
			}
		}
		for p, c := range files {
			full := filepath.Join(root, filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return nil, nil, err
			}
			if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
				return nil, nil, err
			}
		}
		return nil, nil, nil
	}
}

func regenRepo(t *testing.T) (*dodtest.Repo, string) {
	r := dodtest.NewRepo(t)
	r.Write("pkg/resource/widget/sdk.go", genSDK)
	r.Write("pkg/resource/widget/hook.go", "package widget\n")
	r.Write(metadataPath, metaYAML("v0.64.0", "2026-11-01"))
	return r, r.Commit("head")
}

func runRegen(t *testing.T, files map[string]string, fail bool) (Check, gitRepo) {
	t.Helper()
	r, head := regenRepo(t)
	s, cleanup, err := cloneScratch(context.Background(), gitRepo{dir: r.Dir}, head)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	var log bytes.Buffer
	c := checkRegen(context.Background(), s, head, regenOptions{Service: "widget", CodegenDir: t.TempDir(), Run: fakeMake(files, fail), Log: &log})
	return c, s
}

func TestRegenClean(t *testing.T) {
	ok := map[string]string{"pkg/resource/widget/sdk.go": genSDK, metadataPath: metaYAML("v0.64.0", "2026-11-16")}
	c, s := runRegen(t, ok, false)
	assert.Equal(t, StatusPass, c.Status, "%+v", c.Findings)
	// The scratch clone is back at head afterwards.
	got, err := os.ReadFile(filepath.Join(s.dir, "pkg/resource/widget/sdk.go"))
	require.NoError(t, err)
	assert.Equal(t, genSDK, string(got))

	for name, tc := range map[string]struct {
		files map[string]string
		fail  bool
		want  string
	}{
		"not reproduced": {map[string]string{metadataPath: metaYAML("v0.64.0", "2026-11-16")}, false, "GENERATED_FILE_NOT_REPRODUCED"},
		"differs":        {map[string]string{"pkg/resource/widget/sdk.go": genSDK + "var X = 1\n", metadataPath: metaYAML("v0.64.0", "x")}, false, "REGEN_DIFF"},
		"new file":       {map[string]string{"pkg/resource/widget/sdk.go": genSDK, "pkg/resource/widget/extra.go": genSDK}, false, "REGEN_NEW_FILE"},
		"codegen moved":  {map[string]string{"pkg/resource/widget/sdk.go": genSDK, metadataPath: metaYAML("v0.65.0", "x")}, false, "REGEN_CODEGEN_VERSION_MISMATCH"},
		"make fails":     {nil, true, "REGEN_FAILED"},
	} {
		c, s := runRegen(t, tc.files, tc.fail)
		assert.Equal(t, []string{tc.want}, c.Reasons, name)
		assert.NoFileExists(t, filepath.Join(s.dir, "pkg/resource/widget/extra.go"), name)
	}

	assert.Equal(t, StatusSkip, checkRegen(context.Background(), gitRepo{}, "", regenOptions{}).Status)
}

func TestRegenDirtyCodegen(t *testing.T) {
	r := dodtest.NewRepo(t)
	r.Write(metadataPath, metaYAML("v0.64.0-dirty", "a"))
	head := r.Commit("head")
	s, cleanup, err := cloneScratch(context.Background(), gitRepo{dir: r.Dir}, head)
	require.NoError(t, err)
	defer cleanup()
	c := checkRegen(context.Background(), s, head, regenOptions{Service: "w", CodegenDir: t.TempDir(),
		Run: fakeMake(map[string]string{metadataPath: metaYAML("v0.64.0", "b")}, false), Log: &bytes.Buffer{}})
	assert.Equal(t, []string{"REGEN_DIRTY_CODEGEN"}, c.Reasons)
}

// build-controller-release.sh looks for ack-generate at a GOPATH-relative
// path, so a code-generator checkout anywhere else must be told where the
// binary build-controller just built is.
func TestRegenPassesTheEnvironment(t *testing.T) {
	r, head := regenRepo(t)
	s, cleanup, err := cloneScratch(context.Background(), gitRepo{dir: r.Dir}, head)
	require.NoError(t, err)
	defer cleanup()
	codegen := t.TempDir()
	var got []string
	run := func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, []byte, error) {
		got = env
		return nil, nil, errors.New("stop")
	}
	checkRegen(context.Background(), s, head, regenOptions{Service: "widget", CodegenDir: codegen,
		RuntimeDir: "/rt", CacheDir: "/cache", Run: run, Log: &bytes.Buffer{}})
	assert.ElementsMatch(t, []string{
		"SERVICE=widget", "SERVICE_CONTROLLER_SOURCE_PATH=" + s.dir,
		"ACK_GENERATE_BIN_PATH=" + filepath.Join(codegen, "bin", "ack-generate"),
		"RUNTIME_CRD_DIR=" + filepath.Join("/rt", "config"), "ACK_GENERATE_CACHE_DIR=/cache",
	}, got)
}
