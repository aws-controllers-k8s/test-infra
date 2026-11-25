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
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// PreflightChecks are the checks an agent job runs on its own commit before
// it pushes. They read only git: no PR code runs, and no code-generator,
// ack-generate or SDK model is needed. The fork branch is public, so a leak
// caught after the push is already out.
var PreflightChecks = []string{CheckPathAllowlist, CheckCodeScan, CheckLeaks, CheckSDKPin}

// PreflightOptions are one preflight's inputs. RepoDir is read, never written.
type PreflightOptions struct {
	RepoDir, BaseRef, HeadRef, ChangeClass string
	PRBody                                 []byte
}

// PreflightResult is advisory: it decides only whether the job may push. It
// is not a verdict and is never signed; the ack-dod job still gates the PR.
type PreflightResult struct {
	ChangeClass string   `json:"change_class"`
	BaseSHA     string   `json:"base_sha"`
	HeadSHA     string   `json:"head_sha"`
	Blocked     bool     `json:"blocked"`
	Reasons     []string `json:"reasons"`
	Checks      []Check  `json:"checks"`
}

// preflightBlocks reports whether a check stops the push. A leak or a
// sensitive path always does. Any other binding failure does too, unless the
// check only asks for a higher class: ack-dod then escalates the PR for a
// person, as it would without a preflight.
func preflightBlocks(class string, c Check) bool {
	if c.Status == StatusPass || !binding(class, false, c.ID) {
		return false
	}
	if c.ID == CheckLeaks {
		return true
	}
	for _, f := range c.Findings {
		if f.Fails && f.Signature == "SENSITIVE_PATH" {
			return true
		}
	}
	return c.Reclassify == "" || classRank[c.Reclassify] <= classRank[class]
}

// Preflight runs PreflightChecks on BaseRef (its merge base with HeadRef) ..
// HeadRef. An error means it could not decide; callers must not push.
func Preflight(ctx context.Context, o PreflightOptions) (*PreflightResult, error) {
	switch {
	case o.RepoDir == "":
		return nil, errors.New("--repo-dir must be set")
	case strings.TrimSpace(o.BaseRef) == "":
		return nil, errors.New("--base-ref must be set")
	case !validClass(o.ChangeClass):
		return nil, fmt.Errorf("--change-class %q is not one of %s", o.ChangeClass, strings.Join(InputClasses, ", "))
	}
	dir, err := filepath.Abs(o.RepoDir)
	if err != nil {
		return nil, err
	}
	src := gitRepo{dir: dir}
	head, err := src.commitSHA(ctx, cmp.Or(o.HeadRef, "HEAD"))
	if err != nil {
		return nil, fmt.Errorf("--head-ref: %w", err)
	}
	base, err := src.commitSHA(ctx, o.BaseRef)
	if err != nil {
		return nil, fmt.Errorf("--base-ref: %w", err)
	}
	mb, err := src.mergeBase(ctx, base, head)
	if err != nil {
		return nil, err
	}
	files, err := gather(ctx, src, mb, head)
	if err != nil {
		return nil, err
	}
	commits, err := src.commits(ctx, mb, head)
	if err != nil {
		return nil, err
	}
	var readErr error
	read := func(rev, p string) []byte {
		if readErr != nil {
			return nil
		}
		var b []byte
		b, readErr = src.showIfExists(ctx, rev, p)
		return b
	}
	baseMeta, headMeta := read(mb, metadataPath), read(head, metadataPath)
	baseMod, headMod := read(mb, "go.mod"), read(head, "go.mod")
	if readErr != nil {
		return nil, readErr
	}
	basePins, basePinErr := readPins(baseMeta, baseMod)
	headPins, headPinErr := readPins(headMeta, headMod)

	r := &PreflightResult{ChangeClass: o.ChangeClass, BaseSHA: mb, HeadSHA: head, Reasons: []string{}}
	for _, id := range PreflightChecks {
		var c Check
		switch id {
		case CheckPathAllowlist:
			c = checkPathAllowlist(o.ChangeClass, files)
		case CheckCodeScan:
			c = checkCodeScan(o.ChangeClass, files)
		case CheckLeaks:
			c = checkLeaks(files, commits, o.PRBody)
		case CheckSDKPin:
			c = sdkPinCheck(o.ChangeClass, basePins, basePinErr, headPins, headPinErr)
		}
		c.Binding = binding(o.ChangeClass, false, id)
		c.normalize()
		if preflightBlocks(o.ChangeClass, c) {
			r.Blocked = true
			reasons := c.Reasons
			if len(reasons) == 0 {
				reasons = []string{"NO_REASON"}
			}
			for _, reason := range reasons {
				r.Reasons = append(r.Reasons, c.ID+":"+reason)
			}
		}
		r.Checks = append(r.Checks, c)
	}
	r.Reasons = sortedUnique(r.Reasons)
	return r, nil
}

// WritePreflightText is the human-readable form of a preflight result.
// Findings carry no secret values (leak details are hashes).
func WritePreflightText(w io.Writer, r *PreflightResult) error {
	var b strings.Builder
	for _, c := range r.Checks {
		role := "info"
		if c.Binding {
			role = "binding"
		}
		fmt.Fprintf(&b, "check %-16s %-4s %-7s %s", c.ID, c.Status, role, strings.Join(c.Reasons, ","))
		if c.Reclassify != "" {
			fmt.Fprintf(&b, " reclassify=%s", c.Reclassify)
		}
		b.WriteString("\n")
		for _, f := range c.Findings {
			mark := " "
			if f.Fails {
				mark = "!"
			}
			fmt.Fprintf(&b, "  %s %s %s:%d %s\n", mark, f.Signature, f.File, f.Line, f.Detail)
		}
	}
	status := "pass"
	if r.Blocked {
		status = "blocked"
	}
	fmt.Fprintf(&b, "preflight %s class=%s head=%s reasons=%s\n", status, r.ChangeClass, r.HeadSHA, strings.Join(r.Reasons, ","))
	_, err := io.WriteString(w, b.String())
	return err
}
