package ledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

const itemsDir = "workitems"

// dirs is every state directory, in a fixed order so Lint reports
// deterministically.
var dirs = []string{"done", "escalated", "inflight", "open", "wontfix"}

func decode(path string) (*WorkItem, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var w WorkItem
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// Anything after the object (a second object, a merge-conflict marker) is
	// an error.
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%s: unexpected data after the work item", path)
	}
	return &w, nil
}

// scan walks workitems/ without following links. It returns the regular
// *.json files directly inside a known state directory, in a fixed order, and
// one error per other entry. A missing ledger is empty.
func scan(root string) (paths []string, stray []error, err error) {
	base := filepath.Join(root, itemsDir)
	// WalkDir Lstats the root and never descends a symlink, so check the root
	// ourselves: absent is an empty ledger, anything but a real directory is
	// an error.
	switch fi, lerr := os.Lstat(base); {
	case errors.Is(lerr, fs.ErrNotExist):
		return nil, nil, nil
	case lerr != nil:
		return nil, nil, lerr
	case !fi.IsDir():
		return nil, nil, fmt.Errorf("%s: must be a real directory, not a symlink or file", base)
	}
	err = filepath.WalkDir(base, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			if p == base && errors.Is(werr, fs.ErrNotExist) {
				return nil
			}
			return werr
		}
		if p == base {
			return nil
		}
		rel, _ := filepath.Rel(base, p)
		parts := strings.Split(rel, string(filepath.Separator))
		report := func() {
			stray = append(stray, fmt.Errorf("%s: not a regular .json file directly inside one of %s", p, strings.Join(dirs, ", ")))
		}
		switch {
		case d.IsDir() && len(parts) == 1 && slices.Contains(dirs, parts[0]):
			return nil
		case d.IsDir():
			report()
			return fs.SkipDir
		case len(parts) == 2 && slices.Contains(dirs, parts[0]) && d.Type().IsRegular() && strings.HasSuffix(parts[1], ".json"):
			paths = append(paths, p)
		default:
			report()
		}
		return nil
	})
	return paths, stray, err
}

func itemPaths(root string) ([]string, error) {
	paths, _, err := scan(root)
	return paths, err
}

// Load reads every work item under root, keyed by Key. A missing ledger is
// empty, not an error. Entries that are not item files are left to Lint.
func Load(root string) (map[string]*WorkItem, error) {
	paths, err := itemPaths(root)
	if err != nil {
		return nil, err
	}
	items := map[string]*WorkItem{}
	for _, p := range paths {
		w, err := decode(p)
		if err != nil {
			return nil, err
		}
		if w.KeyHash != KeyHash(w.Key) {
			return nil, fmt.Errorf("%s: key_hash %q does not match key (want %s)", p, w.KeyHash, KeyHash(w.Key))
		}
		if filepath.Base(p) != w.FileName() {
			return nil, fmt.Errorf("%s: file name should be %s", p, w.FileName())
		}
		if _, dup := items[w.Key]; dup {
			return nil, fmt.Errorf("%s: duplicate key %q", p, w.Key)
		}
		items[w.Key] = w
	}
	return items, nil
}

// writeAtomic writes b to dir/name through a temp file in dir and a rename, so
// a crash leaves either the old file or the new one.
func writeAtomic(dir, name string, b []byte) error {
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o644)
	}
	if err == nil {
		err = os.Rename(tmp, filepath.Join(dir, name))
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// Save writes every item into the directory its state names and removes any
// copy left in another directory. Unchanged items are rewritten byte for byte,
// so git sees no change. Every item is validated before anything is written;
// each new file lands before the old copies are removed.
func Save(root string, items map[string]*WorkItem) error {
	keys := make([]string, 0, len(items))
	for k := range items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	encoded := map[string][]byte{}
	names := map[string]string{}
	for _, k := range keys {
		w := items[k]
		if _, ok := w.State.Dir(); !ok {
			return fmt.Errorf("item %q: unknown state %q", k, w.State)
		}
		if w.KeyHash != KeyHash(w.Key) {
			return fmt.Errorf("item %q: key_hash %q does not match key (want %s)", k, w.KeyHash, KeyHash(w.Key))
		}
		// Two keys with one file name would mean a hash collision.
		name := w.FileName()
		if other, dup := names[name]; dup {
			return fmt.Errorf("items %q and %q share file name %s (key hash collision)", other, k, name)
		}
		names[name] = k
		b, err := Marshal(w)
		if err != nil {
			return fmt.Errorf("item %q: %w", k, err)
		}
		encoded[k] = b
	}
	// Refuse to write into a ledger with a symlinked or otherwise unexpected
	// entry, so a hand-edited link cannot redirect a write.
	_, stray, err := scan(root)
	if err != nil {
		return err
	}
	if len(stray) > 0 {
		return fmt.Errorf("refusing to save into a malformed ledger: %w", errors.Join(stray...))
	}
	for _, k := range keys {
		w := items[k]
		dir, _ := w.State.Dir()
		target := filepath.Join(root, itemsDir, dir)
		if err := os.MkdirAll(target, 0o755); err != nil {
			return err
		}
		if err := writeAtomic(target, w.FileName(), encoded[k]); err != nil {
			return err
		}
		for _, other := range dirs {
			if other == dir {
				continue
			}
			err := os.Remove(filepath.Join(root, itemsDir, other, w.FileName()))
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}
