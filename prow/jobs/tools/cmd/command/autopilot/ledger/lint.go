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

package ledger

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Lint checks every file under root and returns one error per problem. Jobs
// run it after writing and before pushing, so a bad ledger never lands.
func Lint(root string) []error {
	paths, errs, err := scan(root)
	if err != nil {
		return []error{err}
	}
	seen := map[string]string{}
	for _, p := range paths {
		w, err := decode(p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		fail := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("%s: %s", p, fmt.Sprintf(format, args...)))
		}
		dir := filepath.Base(filepath.Dir(p))
		switch want, ok := w.State.Dir(); {
		case !ok:
			fail("unknown state %q", w.State)
		case want != dir:
			fail("state %s belongs in workitems/%s", w.State, want)
		}
		if w.Schema != Schema {
			fail("schema %q, want %q", w.Schema, Schema)
		}
		switch {
		case w.Key == "":
			fail("key is empty")
		// encoding/json replaces invalid bytes with U+FFFD on decode, so
		// utf8.ValidString cannot fail here; the U+FFFD check is what actually
		// catches a key that held invalid bytes. ValidString stays as a guard.
		case !utf8.ValidString(w.Key) || strings.ContainsRune(w.Key, utf8.RuneError):
			fail("key is not valid UTF-8")
		case strings.IndexFunc(w.Key, unicode.IsControl) >= 0:
			fail("key holds a control character")
		}
		if w.Service == "" || w.Source == "" {
			fail("service and source are required")
		}
		if w.KeyHash != KeyHash(w.Key) {
			fail("key_hash %s does not match key (want %s)", w.KeyHash, KeyHash(w.Key))
		} else if filepath.Base(p) != w.FileName() {
			fail("file name should be %s", w.FileName())
		}
		if n := len(w.History); n == 0 || w.History[n-1].To != w.State {
			fail("last history entry must move the item to its state %s", w.State)
		}
		for _, h := range w.History {
			if !utcRFC3339(h.TS) {
				fail("history ts %q is not RFC 3339 UTC", h.TS)
				break
			}
		}
		switch {
		case w.State.ActiveRun() && (w.Claim == nil || w.Claim.LaunchID == "" || w.Claim.Workflow == "" || w.Attempts == nil):
			fail("state %s needs a claim with a launch_id and workflow, and attempts", w.State)
		case !w.State.ActiveRun() && w.Claim != nil:
			fail("state %s must not hold a claim; only RUNNING, GATING and RETRY_ENV do", w.State)
		}
		if c := w.Claim; c != nil {
			tsOK, dlOK := utcRFC3339(c.TS), utcRFC3339(c.Deadline)
			if !tsOK {
				fail("claim ts %q is not RFC 3339 UTC", c.TS)
			}
			if !dlOK {
				fail("claim deadline %q is not RFC 3339 UTC", c.Deadline)
			}
			if tsOK && dlOK && c.Deadline <= c.TS {
				fail("claim deadline %s is not after ts %s", c.Deadline, c.TS)
			}
			if c.LaunchID != "" && !slices.Contains(w.Links.ProwJobs, c.LaunchID) {
				fail("claim launch_id %q is not in links.prowjobs", c.LaunchID)
			}
		}
		if a := w.Attempts; a != nil && (a.MaxRuns < 1 || a.Runs < 0 || a.Runs > a.MaxRuns) {
			fail("attempts runs=%d max_runs=%d out of range", a.Runs, a.MaxRuns)
		}
		if u := w.Usage; u != nil && (u.Tokens < 0 || u.E2ERuns < 0) {
			fail("usage tokens=%d e2e_runs=%d is negative", u.Tokens, u.E2ERuns)
		}
		if other, dup := seen[w.Key]; dup {
			fail("duplicate key %q (also in %s)", w.Key, other)
		}
		seen[w.Key] = p
	}
	return errs
}

// utcRFC3339 reports whether s is a UTC timestamp in the exact form
// time.RFC3339 produces. The scheduler compares these strings directly, so an
// offset or fractional seconds would break the ordering.
func utcRFC3339(s string) bool {
	t, err := time.Parse(time.RFC3339, s)
	return err == nil && t.UTC().Format(time.RFC3339) == s
}
