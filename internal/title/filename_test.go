package title

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestFilename(t *testing.T) {
	cases := []struct {
		title, ext string
		max        int
		want       string
	}{
		{"Quarterly Report", "docx", 255, "Quarterly Report.docx"},
		{"Quarterly Report", "DOCX", 255, "Quarterly Report.DOCX"}, // the extension as given
		{"Quarterly Report", ".docx", 255, "Quarterly Report.docx"},
		{"0", "..0", 65, "0.0"}, // found by FuzzFilename: every leading dot goes
		{"Quarterly Report", "", 255, "Quarterly Report"},

		// What some file system forbids.
		{"Chapter 1: Intro", "html", 255, "Chapter 1 - Intro.html"},
		{"TCP/IP Guide", "pdf", 255, "TCP-IP Guide.pdf"},
		{"Routers | Switches", "md", 255, "Routers - Switches.md"},
		{`C:\Windows\System32`, "txt", 255, "C-Windows-System32.txt"},
		{"http://example.com/docs", "html", 255, "http-example.com-docs.html"},
		{"Foo:Bar: Baz", "", 255, "Foo-Bar - Baz"},
		{"Notes:", "md", 255, "Notes.md"},
		{"/etc/hosts", "", 255, "etc-hosts"},
		{"a/./b", "", 255, "a-b"},
		{"What is BGP?", "html", 255, "What is BGP.html"},
		{"**Bold** <T>", "md", 255, "Bold T.md"},
		{`The "Best" Guide`, "html", 255, "The 'Best' Guide.html"},
		{"Tab\there\nnewline\x00nul\x7fdel", "txt", 255, "Tab here newline nul del.txt"},
		{"invoice\xe2\x80\xaefdp.exe", "pdf", 255, "invoicefdp.exe.pdf"}, // a right-to-left override
		{"bad\xffbyte", "txt", 255, "badbyte.txt"},

		// What Windows strips, and what hides a file elsewhere.
		{"  ..Hidden..  ", "txt", 255, "Hidden.txt"},
		{"???", "html", 255, ""},
		{"...", "html", 255, ""},
		{"", "html", 255, ""},

		// Device names.
		{"CON", "txt", 255, "CON_.txt"},
		{"con", "", 255, "con_"},
		{"Nul.Notes", "md", 255, "Nul_.Notes.md"},
		{"COM1", "log", 255, "COM1_.log"},
		{"lpt9", "txt", 255, "lpt9_.txt"},
		{"COM0", "txt", 255, "COM0_.txt"},
		{"COM²", "txt", 255, "COM²_.txt"},
		{"AUX ", "txt", 255, "AUX_.txt"},
		{"COM10", "txt", 255, "COM10.txt"},
		{"CONSOLE", "txt", 255, "CONSOLE.txt"},
		{"CON Manual", "txt", 8, "CON_.txt"},
		{"CON Manual", "txt", 7, "CO.txt"},

		// Length.
		{"Network Setup Guide", "html", 18, "Network Setup.html"},
		{"Chapter 1: Introduction", "", 12, "Chapter 1"},
		{"日本語のタイトルです", "md", 20, "日本語のタ.md"},
		{"Ünïcödé Nämé", "txt", 12, "Ünïcö.txt"},
		{"Cafe\xcc\x81 Menu", "", 5, "Caf"}, // the accent stays with its letter
		{"Title", "html", 5, ""},
		{"Title", "html", 0, ""},
		{"Title", "html", -1, ""},

		// An extension no file system takes.
		{"Title", "ht/ml", 255, ""},
		{"Title", "html ", 255, ""},
		{"Title", "html.", 255, ""},
		{"Title", "ht\xffml", 255, ""},
		{"Title", "ht\tml", 255, ""},
	}
	for _, c := range cases {
		if got := Filename(c.title, c.ext, c.max); got != c.want {
			t.Errorf("Filename(%q, %q, %d) = %q, want %q", c.title, c.ext, c.max, got, c.want)
		}
	}
}

// TestFilenameAtEveryLength cuts multi-byte titles at every length and checks
// each result against the promises Filename makes.
func TestFilenameAtEveryLength(t *testing.T) {
	titles := []string{
		"Router: Configuración Avanzada / Guía",
		"日本語のタイトル: 設定ガイド",
		"Team \xf0\x9f\x91\xa8\xe2\x80\x8d\xf0\x9f\x91\xa9\xe2\x80\x8d\xf0\x9f\x91\xa7 Notes",
		"CON.txt",
	}
	for _, title := range titles {
		for max := -1; max <= len(title)+8; max++ {
			checkFilename(t, title, "html", max)
		}
	}
}

// checkFilename fails when Filename(title, ext, max) breaks a promise: a name
// legal on every system, within max bytes, ending in the extension, and stable
// when fed back in.
func checkFilename(t *testing.T, title, ext string, max int) {
	t.Helper()
	got := Filename(title, ext, max)
	if got == "" {
		return
	}
	fail := func(why string) {
		t.Helper()
		t.Fatalf("Filename(%q, %q, %d) = %q: %s", title, ext, max, got, why)
	}
	if len(got) > max {
		fail("longer than max")
	}
	if !utf8.ValidString(got) {
		fail("not valid UTF-8")
	}
	if strings.ContainsAny(got, forbidden) {
		fail("holds a forbidden character")
	}
	if strings.ContainsFunc(got, func(r rune) bool { return unicode.IsControl(r) || invisible(r) }) {
		fail("holds a control or invisible character")
	}
	if strings.Trim(got, " .") != got {
		fail("starts or ends with a space or dot")
	}
	if reserved(got) {
		fail("is a device name")
	}
	ext = strings.TrimLeft(ext, ".")
	stem := got
	if ext != "" {
		if !strings.HasSuffix(got, "."+ext) {
			fail("lost its extension")
		}
		stem = strings.TrimSuffix(got, "."+ext)
	}
	if stem == "" || strings.Trim(stem, " .") != stem {
		fail("has an empty or untrimmed name before its extension")
	}
	if again := Filename(stem, ext, max); again != got {
		t.Fatalf("Filename(%q, %q, %d) = %q, but fed back in it gives %q", title, ext, max, got, again)
	}
}

func FuzzFilename(f *testing.F) {
	f.Add("Chapter 1: Intro", "html", 255)
	f.Add("CON", "txt", 8)
	f.Add("日本語のタイトル: 設定ガイド", "md", 20)
	f.Add("  ..Hidden..  ", "", 10)
	f.Add("a/./b\\c|d", ".pdf", 6)
	f.Add("Team \xf0\x9f\x91\xa8\xe2\x80\x8d\xf0\x9f\x91\xa9 Notes", "html", 15)
	f.Add("Cafe\xcc\x81", "", 4)
	f.Add("invoice\xe2\x80\xaefdp.exe", "pdf", 255)
	f.Fuzz(func(t *testing.T, title, ext string, max int) {
		checkFilename(t, title, ext, max)
	})
}
