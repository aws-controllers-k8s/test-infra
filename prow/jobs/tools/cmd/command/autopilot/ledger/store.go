package ledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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
	return &w, nil
}

func itemPaths(root string) ([]string, error) {
	var all []string
	for _, d := range dirs {
		paths, err := filepath.Glob(filepath.Join(root, itemsDir, d, "*.json"))
		if err != nil {
			return nil, err
		}
		sort.Strings(paths)
		all = append(all, paths...)
	}
	return all, nil
}

// Load reads every work item under root, keyed by Key. A missing ledger is
// empty, not an error.
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
		if _, dup := items[w.Key]; dup {
			return nil, fmt.Errorf("%s: duplicate key %q", p, w.Key)
		}
		items[w.Key] = w
	}
	return items, nil
}

// Save writes every item into the directory its state names and removes any
// copy left in another directory. Unchanged items are rewritten byte for byte,
// so git sees no change.
func Save(root string, items map[string]*WorkItem) error {
	keys := make([]string, 0, len(items))
	for k := range items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w := items[k]
		dir, ok := w.State.Dir()
		if !ok {
			return fmt.Errorf("item %q: unknown state %q", k, w.State)
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
		b, err := Marshal(w)
		if err != nil {
			return fmt.Errorf("item %q: %w", k, err)
		}
		target := filepath.Join(root, itemsDir, dir)
		if err := os.MkdirAll(target, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(target, w.FileName()), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}
