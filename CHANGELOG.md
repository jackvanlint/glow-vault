# Changelog

## glow-vault — fork of charmbracelet/glow

### v0.1.0 — 2026-05-21

**Fork purpose:** replace the flat file listing with a navigable folder tree, making the TUI usable for large vaults (e.g. Obsidian).

#### Changes from upstream

- `ui/tree.go` *(new)* — `treeEntry` type and `buildDirIndex()` which maps each directory path to its sorted children (dirs before files, alphabetical).
- `ui/stash.go` — stash model extended with `dirIndex` and `currentDir` state; file listing now driven by `currentEntries()` / `selectedEntry()` instead of the flat `getVisibleMarkdowns()` slice; navigation keys `l`/`→` enter a directory or open a file, `h`/`←` go up one level; header shows the current path instead of a document count; filter (`/`) searches by name within the current directory only; tree is rebuilt whenever the file scan completes or the user reloads (`r`).
- `ui/stashitem.go` — added `treeEntryView()` which renders directory entries (grey, `/`-suffixed) and file entries (name + relative timestamp) with the same selected/unselected colour logic as the original stash items.
- `ui/ui.go` — `r` reload now also clears `dirIndex` and resets `currentDir` to root.

#### Behaviour

| Key | Action |
|-----|--------|
| `j` / `↓` | move down |
| `k` / `↑` | move up |
| `l` / `→` / `Enter` | enter directory or open file |
| `h` / `←` | go up one directory |
| `/` | filter entries in current directory by name |
| `Esc` | clear filter |
| `e` | open file in `$EDITOR` |
| `r` | reload all files from disk |
| `q` | quit |

#### Not changed

Pager, glamour rendering, search, mouse support, config, all upstream flags — unchanged from `charmbracelet/glow v2.1.2`.

---

Upstream: https://github.com/charmbracelet/glow  
Fork base: `5378827` (v2.1.2 + chore: remove CODEOWNERS)
