package organize

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Table classifies extensions into categories, and folds aliases into one folder.
//
// The built-in table suits most trees. A user whose work differs — a lab that
// wants .fasta under Data, an archivist who wants .chm with Archives — writes
// categories.json in the config folder and it replaces the built-in table.
// `superdirectory categories --write` saves the current table there to start from.
type Table struct {
	byExt   map[string]string // extension -> category
	aliases map[string]string // extension -> the extension whose folder it shares
}

// defaultTable is the built-in table.
var defaultTable = newTable(categoryExts, aliasExts)

// DefaultTable is the built-in table.
func DefaultTable() *Table { return defaultTable }

func newTable(categories map[string][]string, aliases map[string]string) *Table {
	t := &Table{byExt: map[string]string{}, aliases: map[string]string{}}
	for cat, exts := range categories {
		for _, e := range exts {
			t.byExt[e] = cat
		}
	}
	for from, to := range aliases {
		t.aliases[from] = to
	}
	return t
}

// Category is the extension's category, or CategoryOther.
func (t *Table) Category(ext string) string {
	if c, ok := t.byExt[ext]; ok {
		return c
	}
	return CategoryOther
}

// Folder is the extension folder a file of this extension goes into.
func (t *Table) Folder(ext string) string {
	if to, ok := t.aliases[ext]; ok {
		return to
	}
	return ext
}

// Categories lists the table's categories, sorted, without CategoryOther.
func (t *Table) Categories() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range t.byExt {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// tableFile is categories.json: both sections optional, each replacing its
// built-in counterpart when present.
type tableFile struct {
	Categories map[string][]string `json:"categories,omitempty"`
	Aliases    map[string]string   `json:"aliases,omitempty"`
}

// LoadTable reads a categories file. A missing file is not an error: it means the
// built-in table.
func LoadTable(path string) (*Table, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaultTable, nil
	}
	if err != nil {
		return nil, err
	}
	var f tableFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	cats, aliases := categoryExts, aliasExts
	if f.Categories != nil {
		cats = map[string][]string{}
		for name, exts := range f.Categories {
			if err := checkCategory(name); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			for _, e := range exts {
				e = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(e), "."))
				if e == "" || strings.ContainsAny(e, `/\`) {
					return nil, fmt.Errorf("%s: %q is not an extension", path, e)
				}
				cats[name] = append(cats[name], e)
			}
		}
	}
	if f.Aliases != nil {
		aliases = map[string]string{}
		for from, to := range f.Aliases {
			from = strings.ToLower(strings.TrimPrefix(from, "."))
			to = strings.ToLower(strings.TrimPrefix(to, "."))
			if from == "" || to == "" || strings.ContainsAny(from+to, `/\`) {
				return nil, fmt.Errorf("%s: alias %q -> %q is not a pair of extensions", path, from, to)
			}
			aliases[from] = to
		}
	}
	return newTable(cats, aliases), nil
}

// checkCategory refuses category names that cannot be a folder on every system.
func checkCategory(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || trimmed != name || strings.ContainsAny(name, `/\:*?"<>|`) || name == "." || name == ".." {
		return fmt.Errorf("%q cannot be a folder name", name)
	}
	return nil
}

// JSON renders the table as a categories file, sorted, for a user to edit.
func (t *Table) JSON() []byte {
	f := tableFile{Categories: map[string][]string{}, Aliases: map[string]string{}}
	for ext, cat := range t.byExt {
		f.Categories[cat] = append(f.Categories[cat], ext)
	}
	for _, exts := range f.Categories {
		sort.Strings(exts)
	}
	for from, to := range t.aliases {
		f.Aliases[from] = to
	}
	data, _ := json.MarshalIndent(f, "", "  ") // maps marshal with sorted keys
	return append(data, '\n')
}
