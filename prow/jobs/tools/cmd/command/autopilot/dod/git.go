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
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// gitRepo runs git in one directory with the user's display settings
// neutralised, so output is the same on every machine.
type gitRepo struct{ dir string }

func (g gitRepo) run(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"-c", "core.quotePath=false", "-c", "color.ui=never", "-C", g.dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func (g gitRepo) line(ctx context.Context, args ...string) (string, error) {
	out, err := g.run(ctx, args...)
	return strings.TrimSpace(string(out)), err
}

func (g gitRepo) commitSHA(ctx context.Context, rev string) (string, error) {
	return g.line(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
}

func (g gitRepo) treeSHA(ctx context.Context, rev string) (string, error) {
	return g.line(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{tree}")
}

func (g gitRepo) mergeBase(ctx context.Context, a, b string) (string, error) {
	return g.line(ctx, "merge-base", "--end-of-options", a, b)
}

// show returns a blob; the caller knows from the diff status that it exists.
func (g gitRepo) show(ctx context.Context, rev, path string) ([]byte, error) {
	return g.run(ctx, "cat-file", "blob", rev+":"+path)
}

// showIfExists returns nil, nil when rev has no such path.
func (g gitRepo) showIfExists(ctx context.Context, rev, path string) ([]byte, error) {
	out, err := g.run(ctx, "ls-tree", "--name-only", "-z", rev, "--", path)
	if err != nil || len(out) == 0 {
		return nil, err
	}
	return g.show(ctx, rev, path)
}

type fileChange struct {
	Path   string
	Status byte // 'A', 'M', 'D' or 'T'
}

// diffFiles lists changed paths in git's (path) order. Renames are a delete
// and an add, so both paths face the allowlist.
func (g gitRepo) diffFiles(ctx context.Context, base, head string) ([]fileChange, error) {
	out, err := g.run(ctx, "diff", "--name-status", "-z", "--no-renames", "--no-ext-diff", base, head, "--")
	if err != nil {
		return nil, err
	}
	fields := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	var changes []fileChange
	for i := 0; i+1 < len(fields); i += 2 {
		changes = append(changes, fileChange{Path: fields[i+1], Status: fields[i][0]})
	}
	return changes, nil
}

type addedLine struct {
	N    int
	Text string
}

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

func (g gitRepo) addedLines(ctx context.Context, base, head string) (map[string][]addedLine, error) {
	out, err := g.run(ctx, "diff", "-U0", "--text", "--no-renames", "--no-ext-diff", "--no-textconv", base, head, "--")
	if err != nil {
		return nil, err
	}
	return parseAddedLines(out), nil
}

// parseAddedLines reads a -U0 diff. "+++ b/<path>" counts as a file header only
// between "diff --git" and the first hunk, so an added line that starts with
// "++" stays a line.
func parseAddedLines(diff []byte) map[string][]addedLine {
	out := map[string][]addedLine{}
	file, n, inHeader := "", 0, false
	for _, line := range strings.Split(string(diff), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			file, inHeader = "", true
		case inHeader && strings.HasPrefix(line, "+++ "):
			file = strings.TrimPrefix(line, "+++ b/")
			if file == line {
				file = "" // "+++ /dev/null"
			}
		case strings.HasPrefix(line, "@@"):
			inHeader = false
			if m := hunkHeader.FindStringSubmatch(line); m != nil {
				n, _ = strconv.Atoi(m[1])
			}
		case !inHeader && file != "" && strings.HasPrefix(line, "+"):
			out[file] = append(out[file], addedLine{N: n, Text: line[1:]})
			n++
		}
	}
	return out
}

type commit struct {
	SHA, AuthorEmail, Message string
	// Merge is a commit with more than one parent.
	Merge bool
}

// commitInfo reads one commit's author email and message.
func (g gitRepo) commitInfo(ctx context.Context, rev string) (commit, error) {
	out, err := g.run(ctx, "log", "-1", "--format=%H%x00%ae%x00%B", "--end-of-options", rev, "--")
	if err != nil {
		return commit{}, err
	}
	parts := strings.SplitN(string(out), "\x00", 3)
	if len(parts) != 3 {
		return commit{}, fmt.Errorf("git log: malformed record")
	}
	return commit{SHA: parts[0], AuthorEmail: parts[1], Message: strings.TrimSpace(parts[2])}, nil
}

// commits lists every commit in base..head, merges included, oldest first.
func (g gitRepo) commits(ctx context.Context, base, head string) ([]commit, error) {
	out, err := g.run(ctx, "log", "--reverse", "--format=%H%x00%P%x00%ae%x00%B%x1e", base+".."+head, "--")
	if err != nil {
		return nil, err
	}
	var cs []commit
	for _, rec := range strings.Split(string(out), "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\x00", 4)
		if len(parts) != 4 {
			return nil, fmt.Errorf("git log: malformed record")
		}
		cs = append(cs, commit{SHA: parts[0], Merge: len(strings.Fields(parts[1])) > 1,
			AuthorEmail: parts[2], Message: strings.TrimSpace(parts[3])})
	}
	return cs, nil
}

// cloneScratch makes a throwaway checkout of head that shares src's objects,
// so base commits resolve and src is never written.
func cloneScratch(ctx context.Context, src gitRepo, head string) (gitRepo, func(), error) {
	dir, err := os.MkdirTemp("", "ack-dod-")
	if err != nil {
		return gitRepo{}, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if _, err := (gitRepo{dir: src.dir}).run(ctx, "clone", "--quiet", "--shared", "--no-checkout", src.dir, dir); err != nil {
		cleanup()
		return gitRepo{}, nil, err
	}
	s := gitRepo{dir: dir}
	if _, err := s.run(ctx, "checkout", "--quiet", "--detach", head); err != nil {
		cleanup()
		return gitRepo{}, nil, err
	}
	return s, cleanup, nil
}

// reset puts a scratch clone back to head, removing everything untracked.
func (g gitRepo) reset(ctx context.Context, head string) error {
	if _, err := g.run(ctx, "reset", "--quiet", "--hard", head); err != nil {
		return err
	}
	_, err := g.run(ctx, "clean", "-fdxq")
	return err
}

// changedFile is one path in the merge-base..head diff, with both sides'
// contents (nil when absent) and the lines the diff adds.
type changedFile struct {
	Path       string
	Status     byte
	Base, Head []byte
	Generated  bool
	Binary     bool
	Added      []addedLine
}

func isBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	return bytes.IndexByte(b, 0) >= 0
}

func gather(ctx context.Context, g gitRepo, base, head string) ([]changedFile, error) {
	changes, err := g.diffFiles(ctx, base, head)
	if err != nil {
		return nil, err
	}
	added, err := g.addedLines(ctx, base, head)
	if err != nil {
		return nil, err
	}
	files := make([]changedFile, 0, len(changes))
	for _, c := range changes {
		f := changedFile{Path: c.Path, Status: c.Status, Added: added[c.Path]}
		if c.Status != 'A' {
			if f.Base, err = g.show(ctx, base, c.Path); err != nil {
				return nil, err
			}
		}
		if c.Status != 'D' {
			if f.Head, err = g.show(ctx, head, c.Path); err != nil {
				return nil, err
			}
		}
		f.Binary = isBinary(f.Base) || isBinary(f.Head)
		f.Generated = generatedFile(f)
		files = append(files, f)
	}
	return files, nil
}

// scratchAtHead errors unless the scratch clone is exactly head: HEAD is head
// and nothing is modified or untracked (ignored files included).
func scratchAtHead(ctx context.Context, g gitRepo, head string) error {
	at, err := g.commitSHA(ctx, "HEAD")
	if err != nil {
		return err
	}
	if at != head {
		return fmt.Errorf("scratch clone is at %s, not %s", at, head)
	}
	out, err := g.run(ctx, "status", "--porcelain=v1", "--untracked-files=all", "--ignored")
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(out)) > 0 {
		return fmt.Errorf("scratch clone is not clean at %s", head)
	}
	return nil
}
