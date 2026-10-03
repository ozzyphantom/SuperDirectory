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
	"fmt"
	"path/filepath"
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

	// Table classifies extensions. Nil means the built-in table.
	Table *Table
}

// Plan walks source and returns the ordered copy plan, skipping excluded
// subtrees. Destination paths are relative to the target directory; execute the
// plan with flatten.Copy, which creates the folders on demand.
func Plan(source string, excluded map[string]bool, opts Options) ([]flatten.Item, error) {
	files, err := flatten.Scan{Sources: []string{source}, Excluded: excluded}.Files()
	if err != nil {
		return nil, err
	}
	return PlanFiles(files, opts), nil
}

// PlanFiles sorts walked files into Category/extension folders. A file's type is
// its Ext when an earlier stage set one (content detection), else its name's.
// Aliases fold spellings of one type into one folder: .jpeg files land in jpg/,
// still named .jpeg.
func PlanFiles(files []flatten.File, opts Options) []flatten.Item {
	t := opts.Table
	if t == nil {
		t = defaultTable
	}
	items := make([]flatten.Item, len(files))
	for i, f := range files {
		name := f.BaseName()
		ext := f.Ext
		if ext == "" {
			ext = Extension(name)
		}
		dir := filepath.Join(t.Category(ext), extFolder(t.Folder(ext)))
		if opts.KeepSourceTree {
			if rel := f.Dir(); rel != "." {
				dir = filepath.Join(dir, rel)
			}
		}
		items[i] = flatten.Item{Src: f.Path, Want: filepath.Join(dir, name), Rel: f.Rel, Size: f.Size, ModTime: f.ModTime}
	}
	flatten.Assign(items)
	return items
}

// PlanByDate sorts walked files into year/month folders: by when a photo or video
// was taken where an earlier stage found that out, else by when the file was last
// modified. Names collide more often here — two cameras both write IMG_0001.JPG —
// and Assign settles that with a suffix as everywhere else.
func PlanByDate(files []flatten.File) []flatten.Item {
	items := make([]flatten.Item, len(files))
	for i, f := range files {
		when := f.Taken
		if when.IsZero() {
			when = f.ModTime
		}
		dir := filepath.Join(fmt.Sprintf("%04d", when.Year()), fmt.Sprintf("%02d", int(when.Month())))
		items[i] = flatten.Item{Src: f.Path, Want: filepath.Join(dir, f.BaseName()), Rel: f.Rel, Size: f.Size, ModTime: f.ModTime}
	}
	flatten.Assign(items)
	return items
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

// Category maps an extension to its bucket in the built-in table, or
// CategoryOther when the extension is unknown. An empty extension is Other.
func Category(ext string) string { return defaultTable.Category(ext) }

// Categories lists every category in the built-in table, sorted. CategoryOther is
// not included: it is the fallback, not a member of the table.
func Categories() []string { return defaultTable.Categories() }

// categoryExts is the built-in table, written the readable way round: one
// category, its extensions.
var categoryExts = map[string][]string{
	"Documents": {
		"pdf", "doc", "docx", "odt", "rtf", "txt", "md", "markdown", "rst",
		"tex", "pages", "epub", "mobi", "azw3", "djvu", "log", "chm", "xps",
		"oxps", "ps",
	},
	"Spreadsheets":  {"xls", "xlsx", "xlsm", "csv", "tsv", "ods", "numbers"},
	"Presentations": {"ppt", "pptx", "odp", "key"},
	"Diagrams":      {"vsd", "vsdx", "vsdm", "drawio", "dia", "graffle"},
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

// aliasExts fold spellings of one type into one folder. Only the folder changes:
// a .jpeg file lands in Images/jpg/ and keeps its name.
var aliasExts = map[string]string{
	"jpeg": "jpg", "jpe": "jpg", "jfif": "jpg",
	"tif": "tiff", "htm": "html", "shtml": "html",
	"yml": "yaml", "markdown": "md", "mdown": "md",
	"mpeg": "mpg", "aif": "aiff", "midi": "mid", "tgz": "tar.gz",
}
