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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/autopilot/dod"
)

// gitOps runs git in one repository through a dod.Runner, so tests can see
// (and the push guard can be checked on) every command.
type gitOps struct {
	dir string
	run dod.Runner
}

// gitEnv never lets git prompt; GIT_ASKPASS (set by the script) is inherited.
var gitEnv = []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1"}

const stderrTail = 2 << 10

func (g gitOps) raw(ctx context.Context, env []string, args ...string) ([]byte, error) {
	out, stderr, err := g.run(ctx, g.dir, append(append([]string{}, gitEnv...), env...), "git", args...)
	if err != nil {
		if len(stderr) > stderrTail {
			stderr = stderr[len(stderr)-stderrTail:]
		}
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, bytes.TrimSpace(stderr))
	}
	return out, nil
}

func (g gitOps) git(ctx context.Context, args ...string) (string, error) {
	out, err := g.raw(ctx, nil, args...)
	return strings.TrimSpace(string(out)), err
}

func (g gitOps) revParse(ctx context.Context, rev string) (string, error) {
	return g.git(ctx, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
}

func (g gitOps) show(ctx context.Context, rev, path string) ([]byte, error) {
	return g.raw(ctx, nil, "show", rev+":"+path)
}

// showIfExists is show, with nil for a path that is not in rev.
func (g gitOps) showIfExists(ctx context.Context, rev, path string) ([]byte, error) {
	if _, err := g.raw(ctx, nil, "cat-file", "-e", rev+":"+path); err != nil {
		return nil, nil
	}
	return g.show(ctx, rev, path)
}

func (g gitOps) dirty(ctx context.Context) (bool, error) {
	s, err := g.git(ctx, "status", "--porcelain=v1", "--untracked-files=all")
	return s != "", err
}

func (g gitOps) checkout(ctx context.Context, rev string) error {
	_, err := g.git(ctx, "checkout", "--quiet", "--force", "--detach", rev)
	return err
}

// commitBump commits the whole working tree on top of base as the bot, dated
// with base's committer date, so the same tree always gives the same commit.
func (g gitOps) commitBump(ctx context.Context, base, msg, name, email string) (string, error) {
	date, err := g.git(ctx, "show", "-s", "--format=%cI", base)
	if err != nil {
		return "", err
	}
	if _, err := g.git(ctx, "add", "--all"); err != nil {
		return "", err
	}
	env := []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email, "GIT_COMMITTER_DATE=" + date,
	}
	if _, err := g.raw(ctx, env, "-c", "commit.gpgsign=false", "commit", "--quiet", "--no-verify", "-m", msg); err != nil {
		return "", err
	}
	return g.revParse(ctx, "HEAD")
}

// remoteBranch fetches the service's bump branch from remote into a private
// ref and returns its head, or exists=false when the branch is not there.
func (g gitOps) remoteBranch(ctx context.Context, remote, service string) (string, bool, error) {
	ref := BranchRef(service)
	out, err := g.git(ctx, "ls-remote", "--heads", remote, ref)
	if err != nil {
		return "", false, err
	}
	if out == "" {
		return "", false, nil
	}
	if _, err := g.git(ctx, "fetch", "--quiet", "--no-tags", remote, "+"+ref+":refs/autopilot/remote-sdk-bump"); err != nil {
		return "", false, err
	}
	head, err := g.revParse(ctx, "refs/autopilot/remote-sdk-bump")
	return head, err == nil, err
}

// foreignCommits lists the commits on head since its merge base with base
// that the bot did not both author and commit.
func (g gitOps) foreignCommits(ctx context.Context, base, head, botEmail string) ([]string, error) {
	mb, err := g.git(ctx, "merge-base", base, head)
	if err != nil {
		return nil, err
	}
	out, err := g.git(ctx, "log", "--format=%H%x09%ae%x09%ce", mb+".."+head)
	if err != nil {
		return nil, err
	}
	var foreign []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		if !strings.EqualFold(f[1], botEmail) || !strings.EqualFold(f[2], botEmail) {
			foreign = append(foreign, f[0])
		}
	}
	return foreign, nil
}

var volatileLine = regexp.MustCompile(`^[+-]\s*build_date:`)

// sameBump reports whether remote is one commit on base whose tree differs
// from cand only in build_date: lines of the generation metadata, which
// ack-generate stamps with the clock on every run.
func (g gitOps) sameBump(ctx context.Context, base, remote, cand, metadataPath string) (bool, error) {
	parents, err := g.git(ctx, "rev-list", "--parents", "-n", "1", remote)
	if err != nil {
		return false, err
	}
	if f := strings.Fields(parents); len(f) != 2 || f[1] != base {
		return false, nil
	}
	names, err := g.git(ctx, "diff", "--name-only", remote, cand)
	if err != nil {
		return false, err
	}
	switch names {
	case "":
		return true, nil
	case metadataPath:
	default:
		return false, nil
	}
	diff, err := g.git(ctx, "diff", "-U0", "--no-ext-diff", remote, cand, "--", metadataPath)
	if err != nil {
		return false, err
	}
	for _, l := range strings.Split(diff, "\n") {
		if strings.HasPrefix(l, "+++") || strings.HasPrefix(l, "---") {
			continue
		}
		if (strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-")) && !volatileLine.MatchString(l) {
			return false, nil
		}
	}
	return true, nil
}

// worktree checks rev of repo out in a new temporary directory and returns a
// function that removes it again.
func worktree(ctx context.Context, run dod.Runner, repo, rev string) (string, func(), error) {
	g := gitOps{dir: repo, run: run}
	if _, err := g.revParse(ctx, rev); err != nil {
		return "", nil, fmt.Errorf("%s has no revision %q: %w", repo, rev, err)
	}
	dir, err := os.MkdirTemp("", "sdk-bump-worktree-")
	if err != nil {
		return "", nil, err
	}
	if _, err := g.git(ctx, "worktree", "add", "--quiet", "--force", "--detach", dir, rev); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	done := func() {
		_, _ = g.git(context.Background(), "worktree", "remove", "--force", dir)
		os.RemoveAll(dir)
	}
	return dir, done, nil
}

// push moves the service's bump branch on remote to sha, only if it is still
// at old ("" = must not exist). The refspec comes from pushRefspec alone.
func (g gitOps) push(ctx context.Context, remote, service, sha, old string) error {
	spec, err := pushRefspec(service, sha)
	if err != nil {
		return err
	}
	if old != "" && !shaRE.MatchString(old) {
		return errors.New("push: the lease must be a commit SHA or empty")
	}
	_, err = g.git(ctx, "push", "--quiet", "--no-verify", "--porcelain",
		"--force-with-lease="+BranchRef(service)+":"+old, remote, spec)
	return err
}
