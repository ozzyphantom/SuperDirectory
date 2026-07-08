package fsmeta

import "testing"

func TestIsMetadata(t *testing.T) {
	metadata := []string{
		// macOS
		".DS_Store", ".ds_store", ".localized",
		".Spotlight-V100", ".fseventsd", ".Trashes", ".TemporaryItems",
		".DocumentRevisions-V100", ".AppleDouble",
		// AppleDouble sidecars: any name, matched by prefix
		"._receipt.pdf", "._Photos", "._",
		// Windows
		"Thumbs.db", "thumbs.db", "desktop.ini", "$RECYCLE.BIN",
		"System Volume Information",
		// Linux
		".Trash-1000",
	}
	for _, name := range metadata {
		if !IsMetadata(name) {
			t.Errorf("IsMetadata(%q) = false, want true", name)
		}
	}

	content := []string{
		// Ordinary files must survive.
		"receipt.pdf", "beach.JPG", "LICENSE", "notes.txt",
		// Dotfiles are user content, not filesystem bookkeeping.
		".gitignore", ".env", ".git", ".config",
		// Near-misses: a leading dot alone is not enough.
		".ds_storey", "ds_store", "my.DS_Store.backup",
		// A single leading underscore is not an AppleDouble prefix.
		"_notes.txt", "_.txt",
		// "trash" as a real folder name.
		"Trash Talk", "recycler-parts.pdf",
	}
	for _, name := range content {
		if IsMetadata(name) {
			t.Errorf("IsMetadata(%q) = true, want false — user content was skipped", name)
		}
	}
}
