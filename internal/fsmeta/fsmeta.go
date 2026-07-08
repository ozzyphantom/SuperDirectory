// Package fsmeta identifies filesystem bookkeeping — files and directories the
// operating system creates for itself, which no user ever means to copy.
//
// It exists because of external drives. A Mac writes AppleDouble "._" sidecars,
// .DS_Store, .Spotlight-V100/ and .fseventsd/ onto any volume it touches; Windows
// leaves Thumbs.db and $RECYCLE.BIN/. Flattening the root of a drive that has seen
// both can produce a superdirectory where the bookkeeping outnumbers the real
// files two to one.
//
// This is a shared predicate rather than a method on any one package so that the
// copy (package flatten), the exclusion tree, and the wizard's file counts all
// agree about what exists. If only the copy filtered, the exclusion tree would
// offer to skip directories that were never going to be copied, and its counts
// would promise files that never arrive.
package fsmeta

import "strings"

// names are matched case-insensitively against a single path element. Both files
// and directories appear here; the caller decides whether to skip a file or prune
// a whole subtree.
var names = map[string]bool{
	// macOS
	".ds_store":               true,
	".localized":              true,
	".appledouble":            true,
	".apdisk":                 true,
	".spotlight-v100":         true,
	".fseventsd":              true,
	".documentrevisions-v100": true,
	".temporaryitems":         true,
	".trashes":                true,
	".vol":                    true,

	// Windows
	"thumbs.db":                 true,
	"ehthumbs.db":               true,
	"desktop.ini":               true,
	"$recycle.bin":              true,
	"recycler":                  true,
	"system volume information": true,

	// Linux
	".trash":      true,
	".trash-1000": true,
}

// IsMetadata reports whether name — one path element, not a full path — is
// filesystem bookkeeping rather than user content.
//
// The "._" prefix catches AppleDouble sidecars, which macOS creates per-file on
// any volume that cannot store extended attributes natively (exFAT, FAT32). They
// are invisible in Finder but are ordinary files to a directory walk, so a copy
// that does not filter them will faithfully reproduce every one. The cost of this
// rule is that a genuine file named "._notes.txt" would be skipped; nobody names
// files that way, and the alternative is copying thousands of sidecars.
func IsMetadata(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "._") {
		return true
	}
	return names[lower]
}
