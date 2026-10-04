package sniff

import "testing"

// TestAgreesAliasGroups: every spelling in a group fits every other, both ways.
func TestAgreesAliasGroups(t *testing.T) {
	groups := [][]string{
		{"jpg", "jpeg", "jpe", "jfif"},
		{"tif", "tiff"},
		{"htm", "html", "xhtml", "shtml"},
		{"mp4", "m4v"},
		{"m4a", "m4b", "aac"},
		{"mov", "qt"},
		{"mid", "midi"},
		{"aif", "aiff"},
		{"yml", "yaml"},
		{"gz", "tgz"},
	}
	for _, g := range groups {
		for _, ext := range g {
			for _, detected := range g {
				if !Agrees(ext, detected) {
					t.Errorf("Agrees(%q, %q) = false, want true", ext, detected)
				}
			}
		}
	}
}

// TestAgreesFamilies: a detected container or base format fits the formats
// built on it.
func TestAgreesFamilies(t *testing.T) {
	fit := map[string][]string{
		"txt": {
			"txt", "text", "log", "md", "markdown", "rst", "csv", "tsv", "json", "xml", "yaml", "yml",
			"ini", "cfg", "conf", "toml", "go", "py", "js", "ts", "c", "h", "java", "sh", "sql", "css",
			"svg", "html", "htm", "vtt", "srt", "gradle", "mod", "plist", "pem", "key", "pub", "out",
			"dot", "pot", "obj", "cgi", "raw", "dia", "pgo", "1", "360",
		},
		"xml":    {"xml", "svg", "xhtml", "plist", "rss", "atom", "kml", "gpx", "xsd", "xsl", "csproj", "txt", "dia", "graffle"},
		"html":   {"html", "htm", "php", "md", "txt", "aspx"},
		"zip":    {"zip", "jar", "apk", "docx", "xlsx", "pptx", "odt", "ods", "odp", "epub", "ipa", "xpi", "kmz", "pages", "numbers", "key", "whl", "nupkg", "z01"},
		"ole":    {"doc", "xls", "ppt", "msg", "msi", "vsd", "pub", "dot", "xlt", "pps"},
		"tiff":   {"tif", "nef", "cr2", "dng", "arw", "orf", "rw2"},
		"pdf":    {"pdf", "ai"},
		"ps":     {"ps", "eps", "ai"},
		"eps":    {"eps", "ps"},
		"mp4":    {"mp4", "m4v", "m4a", "mov", "3gp", "360"},
		"m4a":    {"m4a", "m4b", "aac", "mp4"},
		"docx":   {"docx", "docm", "dotx"},
		"xlsx":   {"xlsx", "xlsm", "xlsb"},
		"pptx":   {"pptx", "ppsx", "potx"},
		"doc":    {"doc", "dot"},
		"jar":    {"jar", "war", "zip"},
		"apk":    {"apk", "aar", "zip"},
		"exe":    {"exe", "dll", "sys", "scr", "efi"},
		"elf":    {"so", "o", "obj", "ko", "6"},
		"macho":  {"dylib", "bundle", "so", "cgi", "34"},
		"webm":   {"webm", "mkv"},
		"mkv":    {"mkv", "mka"},
		"opus":   {"opus", "ogg"},
		"ttf":    {"ttf", "otf"},
		"otf":    {"otf", "ttf"},
		"heic":   {"heic", "heif", "hif"},
		"gz":     {"gz", "tgz", "tar.gz", "svgz", "dia", "pprof"},
		"bz2":    {"bz2", "tar.bz2", "tbz2"},
		"7z":     {"7z", "001", "002"},
		"rar":    {"rar", "r00", "r42", "cbr"},
		"sqlite": {"sqlite", "db"},
	}
	for detected, exts := range fit {
		for _, ext := range exts {
			if !Agrees(ext, detected) {
				t.Errorf("Agrees(%q, %q) = false, want true", ext, detected)
			}
		}
	}
	for detected, exts := range families {
		for _, ext := range exts {
			if !Agrees(ext, detected) {
				t.Errorf("Agrees(%q, %q) = false, but families lists it", ext, detected)
			}
		}
	}
}

// TestAgreesFindsMisnamedFiles: a name that claims another format does not fit,
// which is the point. A PDF named .txt is the case that started it.
func TestAgreesFindsMisnamedFiles(t *testing.T) {
	cases := []struct{ ext, detected string }{
		{"txt", "pdf"},
		{"pdf", "txt"},
		{"pdf", "html"}, // an error page saved under the name of the PDF it replaced
		{"jpg", "png"},
		{"png", "svg"},
		{"docx", "pdf"},
		{"docx", "xml"},
		{"zip", "docx"}, // a document named .zip, which a reader will not open
		{"zip", "epub"},
		{"jar", "apk"},
		{"mp3", "txt"},
		{"mp3", "flac"},
		{"m4a", "mov"},
		{"mov", "mp3"},
		{"exe", "zip"},
		{"doc", "rtf"},
		{"doc", "html"},
		{"xls", "html"},
		{"tar.gz", "tar"}, // not gzipped at all
		{"tgz", "tar"},
		{"opus", "ogg"}, // Vorbis named .opus; Opus named .ogg is fine
		{"webm", "mkv"}, // Matroska named .webm; WebM named .mkv is fine
		{"", "pdf"},
		{"", "html"},
		{"", "zip"},
		{"001", "pdf"},
		{"r00", "zip"},
		{"z01", "rar"},
	}
	for _, c := range cases {
		if Agrees(c.ext, c.detected) {
			t.Errorf("Agrees(%q, %q) = true, want false", c.ext, c.detected)
		}
	}
}

// TestAgreesFormsOfTheArguments: case, a leading dot, and an empty detection.
func TestAgreesFormsOfTheArguments(t *testing.T) {
	cases := []struct{ ext, detected string }{
		{"JPEG", "jpg"},
		{".jpeg", "jpg"},
		{"Pdf", "PDF"},
		{"TAR.GZ", "gz"},
		{"", "txt"},  // README, Makefile
		{"", "json"}, // .eslintrc
		{"", "elf"},
		{"", "macho"},
		{"anything", ""},
		{"", ""},
	}
	for _, c := range cases {
		if !Agrees(c.ext, c.detected) {
			t.Errorf("Agrees(%q, %q) = false, want true", c.ext, c.detected)
		}
	}
	for _, typ := range types {
		if !Agrees(typ, typ) {
			t.Errorf("Agrees(%q, %q) = false: a type must fit its own name", typ, typ)
		}
	}
}
