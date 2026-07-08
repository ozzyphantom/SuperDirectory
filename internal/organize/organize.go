// Package organize is the second planner: instead of collapsing a tree into one
// flat directory, it sorts every file into a "<Category>/<extension>" folder.
//
//	Downloads-super/
//	├── Documents/
//	│   ├── pdf/invoice.pdf
//	│   └── docx/notes.docx
//	├── Images/
//	│   ├── jpg/beach.jpg
//	│   └── png/screenshot.png
//	└── Other/
//	    └── no-extension/LICENSE
//
// With Options.KeepSourceTree the file's original directory nesting is recreated
// inside its extension folder, so provenance survives the reorganization:
//
//	Documents/pdf/Work/Invoices/q3.pdf
//
// Like package flatten, this package is pure and knows nothing about the TUI. It
// shares that package's tree walk (so exclusion semantics cannot drift between
// the two planners), its Item type, and its Copy executor.
package organize

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
)

const (
	// CategoryOther collects every extension absent from the table below.
	CategoryOther = "Other"
	// NoExtension is the folder for files that have no extension at all,
	// including dotfiles such as .gitignore.
	NoExtension = "no-extension"
)

// Options tunes the layout inside each extension folder.
type Options struct {
	// KeepSourceTree recreates each file's original directory nesting inside
	// its extension folder rather than pooling every file of that type in one
	// place. Because a file's path relative to the source is unique, this
	// layout never produces a name collision.
	KeepSourceTree bool
}

// Plan walks source and returns the ordered copy plan, skipping excluded
// subtrees. Destination paths are relative to the target directory; execute the
// plan with flatten.Copy, which creates the folders on demand.
func Plan(source string, excluded map[string]bool, opts Options) ([]flatten.Item, error) {
	var items []flatten.Item
	used := map[string]bool{}

	err := flatten.Walk(source, excluded, func(path string, d os.DirEntry) {
		name := d.Name()
		ext := Extension(name)
		dir := filepath.Join(Category(ext), extFolder(ext))

		if opts.KeepSourceTree {
			if rel := relDir(source, path); rel != "" {
				dir = filepath.Join(dir, rel)
			}
		}
		items = append(items, flatten.Item{
			Src: path,
			Dst: flatten.Unique(used, filepath.Join(dir, name)),
		})
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// relDir returns the file's parent directory relative to source, or "" when the
// file sits in the source root. A path that somehow escapes source (a walk that
// crossed a link, a caller passing unrelated roots) yields "" rather than a
// destination containing "..", which would write outside the target.
func relDir(source, path string) string {
	rel, err := filepath.Rel(source, filepath.Dir(path))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return rel
}

// extFolder is the folder name for an extension.
func extFolder(ext string) string {
	if ext == "" {
		return NoExtension
	}
	return ext
}

// doubleExt are the compound suffixes worth keeping whole. Without this, every
// archive.tar.gz would land in a "gz" folder next to a "bz2" folder, splitting
// tarballs by their compressor rather than grouping them as tarballs.
var doubleExt = []string{"tar.gz", "tar.bz2", "tar.xz", "tar.zst", "tar.lz4"}

// Extension returns the lowercased extension of a filename without its leading
// dot, or "" when the file has none.
//
// A leading dot is a name, not an extension: ".gitignore" and ".env" have no
// extension, and must not create "gitignore" and "env" folders. Go's
// filepath.Ext disagrees — it reads from the final dot, so Ext(".gitignore")
// returns ".gitignore" — which is exactly the trap this function exists to
// avoid.
func Extension(name string) string {
	// Strip one leading dot, then require a *remaining* dot for an extension
	// to exist. Handles ".gitignore" (none) and "README" (none) in one test.
	stem := strings.TrimPrefix(name, ".")
	if !strings.Contains(stem, ".") {
		return ""
	}

	lower := strings.ToLower(stem)
	for _, d := range doubleExt {
		if strings.HasSuffix(lower, "."+d) {
			return d
		}
	}

	// filepath.Ext("file.") is "." and trims to "", which extFolder maps to
	// NoExtension.
	ext := strings.TrimPrefix(filepath.Ext(lower), ".")

	// An extension becomes a directory name. Callers pass os.DirEntry.Name(),
	// which is always a single path element, so this cannot fire today — it is
	// the guard that keeps a future caller from turning a filename into a
	// destination outside the target.
	if strings.ContainsRune(ext, '/') || strings.ContainsRune(ext, filepath.Separator) || strings.Contains(ext, "..") {
		return ""
	}
	return ext
}

// Category maps an extension to its human-facing bucket, or CategoryOther when
// the extension is unknown. An empty extension is Other.
func Category(ext string) string {
	if c, ok := extToCategory[ext]; ok {
		return c
	}
	return CategoryOther
}

// Categories lists every known category name, sorted. CategoryOther is not
// included: it is the fallback, not a member of the table.
func Categories() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range extToCategory {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// categoryExts is the source of truth, written the readable way round: one
// category, its extensions. extToCategory inverts it at init.
var categoryExts = map[string][]string{
	"Documents": {
		"pdf", "doc", "docx", "odt", "rtf", "txt", "md", "markdown", "rst",
		"tex", "pages", "epub", "mobi", "azw3", "djvu", "log",
	},
	"Spreadsheets":  {"xls", "xlsx", "xlsm", "csv", "tsv", "ods", "numbers"},
	"Presentations": {"ppt", "pptx", "odp", "key"},
	"Images": {
		"jpg", "jpeg", "png", "gif", "bmp", "tif", "tiff", "webp", "svg",
		"heic", "heif", "avif", "ico", "psd", "ai", "eps", "raw", "cr2",
		"cr3", "nef", "arw", "dng", "orf", "rw2",
	},
	"Video": {
		"mp4", "mov", "avi", "mkv", "webm", "flv", "wmv", "m4v", "mpg",
		"mpeg", "3gp", "mts", "m2ts",
	},
	"Audio": {
		"mp3", "wav", "flac", "aac", "m4a", "ogg", "oga", "opus", "wma",
		"aiff", "aif", "alac", "mid", "midi",
	},
	"Archives": {
		"zip", "tar", "gz", "tgz", "bz2", "xz", "zst", "7z", "rar", "iso",
		"dmg", "pkg", "deb", "rpm", "tar.gz", "tar.bz2", "tar.xz", "tar.zst",
		"tar.lz4",
	},
	"Code": {
		"go", "py", "js", "mjs", "cjs", "ts", "tsx", "jsx", "java", "kt",
		"c", "h", "cc", "cpp", "hpp", "cs", "rb", "rs", "php", "swift",
		"scala", "clj", "hs", "lua", "pl", "r", "sql", "sh", "bash", "zsh",
		"fish", "ps1", "bat", "vim", "el", "ipynb", "html", "htm", "css",
		"scss", "sass", "less", "json", "jsonc", "yaml", "yml", "toml",
		"xml", "proto", "graphql", "dockerfile", "makefile",
	},
	"Fonts": {"ttf", "otf", "woff", "woff2", "eot"},
}

var extToCategory = func() map[string]string {
	m := make(map[string]string)
	for cat, exts := range categoryExts {
		for _, e := range exts {
			m[e] = cat
		}
	}
	return m
}()
