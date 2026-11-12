package ledger

import (
	"fmt"
	"path/filepath"
)

// Lint checks every file under root and returns one error per problem. Jobs
// run it after writing and before pushing, so a bad ledger never lands.
func Lint(root string) []error {
	paths, err := itemPaths(root)
	if err != nil {
		return []error{err}
	}
	var errs []error
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
		if w.Key == "" || w.Service == "" || w.Source == "" {
			fail("key, service and source are required")
		}
		if w.KeyHash != KeyHash(w.Key) {
			fail("key_hash %s does not match key (want %s)", w.KeyHash, KeyHash(w.Key))
		} else if filepath.Base(p) != w.FileName() {
			fail("file name should be %s", w.FileName())
		}
		if n := len(w.History); n == 0 || w.History[n-1].To != w.State {
			fail("last history entry must move the item to its state %s", w.State)
		}
		if other, dup := seen[w.Key]; dup {
			fail("duplicate key %q (also in %s)", w.Key, other)
		}
		seen[w.Key] = p
	}
	return errs
}
