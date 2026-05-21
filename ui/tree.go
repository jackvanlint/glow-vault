package ui

import (
	"path/filepath"
	"sort"
	"strings"
)

type entryKind int

const (
	entryDir  entryKind = iota
	entryFile
)

type treeEntry struct {
	kind entryKind
	name string
	md   *markdown // only set for entryFile
}

// buildDirIndex returns a map from directory path to its sorted entries.
// The root directory uses key "". Dirs sort before files; both sort alphabetically.
func buildDirIndex(markdowns []*markdown) map[string][]treeEntry {
	dirSet := map[string]bool{}

	for _, md := range markdowns {
		dir := filepath.Dir(md.Note)
		for dir != "." && dir != "" {
			if dirSet[dir] {
				break
			}
			dirSet[dir] = true
			dir = filepath.Dir(dir)
		}
	}

	index := map[string][]treeEntry{}

	for dir := range dirSet {
		parent := filepath.Dir(dir)
		if parent == "." {
			parent = ""
		}
		index[parent] = append(index[parent], treeEntry{
			kind: entryDir,
			name: filepath.Base(dir),
		})
	}

	for _, md := range markdowns {
		dir := filepath.Dir(md.Note)
		if dir == "." {
			dir = ""
		}
		index[dir] = append(index[dir], treeEntry{
			kind: entryFile,
			name: filepath.Base(md.Note),
			md:   md,
		})
	}

	for key, entries := range index {
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].kind != entries[j].kind {
				return entries[i].kind < entries[j].kind
			}
			return strings.ToLower(entries[i].name) < strings.ToLower(entries[j].name)
		})
		index[key] = entries
	}

	return index
}
