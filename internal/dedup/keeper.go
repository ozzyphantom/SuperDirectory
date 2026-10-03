package dedup

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
)

// copyMarks match a file stem, lowercased, that names itself a copy. Each line is
// a convention some tool writes when it duplicates a file.
var copyMarks = []*regexp.Regexp{
	regexp.MustCompile(`^copy of `),                              // Google Drive, older Windows
	regexp.MustCompile(` copy( \d+)?$`),                          // macOS Finder: "beach copy 2"; Windows: "beach - Copy"
	regexp.MustCompile(` - copy \(\d+\)$`),                       // Windows: "beach - Copy (2)"
	regexp.MustCompile(` ?\(\d{1,2}\)$`),                         // browsers, Photos: "beach (1)" — not "(2019)"
	regexp.MustCompile(`\((another |\d+(st|nd|rd|th) )?copy\)$`), // GNOME Files: "beach (copy)"
	regexp.MustCompile(`[_-]copy\d*$`),                           // "beach_copy", "beach-copy2"
	regexp.MustCompile(`conflicted copy`),                        // Dropbox
}

// numbered splits "beach_1", "beach-2" and "beach 3" — a stem with a short numeric
// suffix, as Finder's "Keep Both" and this app's own collision rule write them.
var numbered = regexp.MustCompile(`^(.+?)[ _-](\d{1,3})$`)

// pickKeeper chooses which of a set of duplicates to copy, and returns its plan
// index:
//
//  1. a name that does not read as a copy, over one that does;
//  2. then the shallowest path, nearest the top of the source;
//  3. then plan order, which is the walk's lexical order.
//
// Plan order alone kept "Backup/beach copy.jpg" over "Trip/beach.jpg", because
// "Backup" sorts first. The contents are identical either way; this only decides
// which name and folder survive into the superdirectory.
func pickKeeper(items []flatten.Item, set []int) int {
	stems := make(map[string]bool, len(set))
	for _, i := range set {
		stems[stem(items[i].Src)] = true
	}
	best := set[0]
	bestKey := keeperKey(items, best, stems)
	for _, i := range set[1:] {
		if k := keeperKey(items, i, stems); k.less(bestKey) {
			best, bestKey = i, k
		}
	}
	return best
}

type rank struct {
	copyLike bool
	depth    int
	index    int
}

func (a rank) less(b rank) bool {
	if a.copyLike != b.copyLike {
		return !a.copyLike
	}
	if a.depth != b.depth {
		return a.depth < b.depth
	}
	return a.index < b.index
}

func keeperKey(items []flatten.Item, i int, stems map[string]bool) rank {
	src := items[i].Src
	return rank{
		copyLike: looksLikeCopy(stem(src), stems),
		depth:    strings.Count(filepath.ToSlash(src), "/"),
		index:    i,
	}
}

// looksLikeCopy reports whether a stem names itself a copy, or is another member's
// stem with a numeric suffix ("beach_1" beside "beach"). The second test is
// relative on purpose: "IMG_0001" ends in digits too, and is an original.
func looksLikeCopy(s string, stems map[string]bool) bool {
	for _, re := range copyMarks {
		if re.MatchString(s) {
			return true
		}
	}
	if m := numbered.FindStringSubmatch(s); m != nil && stems[m[1]] {
		return true
	}
	return false
}

// stem is a path's base name without its extension, lowercased.
func stem(path string) string {
	base := filepath.Base(path)
	return strings.ToLower(strings.TrimSuffix(base, filepath.Ext(base)))
}
