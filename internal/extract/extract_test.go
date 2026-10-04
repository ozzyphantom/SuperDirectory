package extract

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMetadataExtractorReadsTheContent(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"doc_4417.html": "<html><head><title>Configuring VLANs</title></head></html>",
		"manual":        "%PDF-1.4\n%%EOF\n",
		"photo.jpg":     "plain words",
	}
	for name, body := range files {
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
	}
	cases := []struct {
		name, typ, title string
		fits             bool
	}{
		{"doc_4417.html", "html", "Configuring VLANs", true},
		{"manual", "pdf", "", false},
		{"photo.jpg", "txt", "", false},
	}
	for _, c := range cases {
		m, err := MetadataExtractor{}.Extract(filepath.Join(dir, c.name))
		if err != nil {
			t.Fatal(err)
		}
		if m.Type != c.typ || m.Fits != c.fits || m.Title != c.title || m.Size != int64(len(files[c.name])) {
			t.Errorf("%s: got %+v", c.name, m)
		}
	}
	if _, err := (MetadataExtractor{}).Extract(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing file gave no error")
	}
}
