package title

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// forbidden are the characters Windows, exFAT and FAT32 refuse in a file name.
// macOS refuses ':' and Linux only '/', so a name without any of them is legal on
// all five.
const forbidden = `/\:*?"<>|`

// Filename turns a title into a safe file name with the given extension: legal on
// macOS, Windows, Linux, exFAT and FAT32, at most max bytes including the
// extension, never splitting a UTF-8 character.
//
// A forbidden character gives way to one that keeps the title readable: ':',
// '/', '\' and '|' become a dash, so "Setup: Part 2" reads "Setup - Part 2" and
// "TCP/IP" reads "TCP-IP"; a double quote becomes a single one; '?', '*', '<' and
// '>' become spaces. A name Windows reserves for a device, such as CON or LPT1,
// gets a "_" after it, with or without an extension, because Windows opens the
// device instead of the file. A long title is cut between words when it can be.
// A title with nothing left to name a file, or an extension no file system takes,
// gives "". Leading dots on ext are dropped, so filepath.Ext's ".html" serves as
// well as "html".
func Filename(title, ext string, max int) string {
	if max <= 0 {
		return ""
	}
	ext = strings.TrimLeft(ext, ".")
	room := max
	if ext != "" {
		if !legalExt(ext) {
			return ""
		}
		room -= 1 + len(ext)
	}
	if room <= 0 {
		return ""
	}
	name := fit(sanitize(title), room)
	if reserved(name) {
		if len(name) >= room {
			name = fit(name, room-1) // make room for the "_"
		}
		if reserved(name) {
			stem, _, _ := strings.Cut(name, ".")
			k := len(strings.TrimRight(stem, " "))
			name = name[:k] + "_" + name[k:]
		}
	}
	if name == "" {
		return ""
	}
	if ext != "" {
		name += "." + ext
	}
	return name
}

// sanitize makes a title legal as a name, apart from its length.
func sanitize(title string) string {
	// First every character on its own: what shows nothing goes, and what some
	// system forbids becomes its stand-in. The separators become sep for now,
	// since how a dash is spaced depends on what surrounds the run.
	const sep = "\x00" // control characters are spaces by now, so this one is free
	var b strings.Builder
	for _, r := range title {
		switch {
		case r == utf8.RuneError || invisible(r):
		case unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("*?<>", r):
			b.WriteByte(' ')
		case r == '"':
			b.WriteByte('\'')
		case strings.ContainsRune(`:/\|`, r):
			b.WriteString(sep)
		default:
			b.WriteRune(r)
		}
	}
	// Then each run of separators becomes one dash: " - " where the title put a
	// space beside the run, as in "Setup: Part 2", and a bare "-" inside a word,
	// as in "TCP/IP". A part with nothing in it is dropped, so "Notes:" loses its
	// colon rather than ending in a dash.
	parts := strings.Split(b.String(), sep)
	var out strings.Builder
	spaced := false
	for k, p := range parts {
		if k > 0 && (strings.HasSuffix(parts[k-1], " ") || strings.HasPrefix(p, " ")) {
			spaced = true
		}
		if strings.Trim(p, " .") == "" {
			continue
		}
		if out.Len() > 0 {
			if spaced {
				out.WriteString(" - ")
			} else {
				out.WriteByte('-')
			}
		}
		out.WriteString(strings.TrimSpace(p))
		spaced = false
	}
	// Windows strips spaces and dots from the end of a name, and a leading dot
	// hides a file everywhere else.
	return strings.Trim(strings.Join(strings.Fields(out.String()), " "), " .")
}

// fit cuts a name to at most n bytes, between words when it can, and trims what
// the cut leaves at the end: spaces and dots, which Windows strips, and a dash
// that no longer separates anything.
func fit(name string, n int) string {
	if len(name) <= n {
		return name
	}
	name = shorten(name, n)
	for {
		t := strings.TrimSuffix(strings.TrimRight(name, " ."), " -")
		if t == name {
			return name
		}
		name = t
	}
}

// reserved reports whether Windows takes name for a device: CON, PRN, AUX, NUL,
// COM0 to COM9 or LPT0 to LPT9, the superscript ¹ ² ³ included, in any case,
// alone or before a dot.
func reserved(name string) bool {
	stem, _, _ := strings.Cut(name, ".")
	stem = strings.ToUpper(strings.TrimRight(stem, " "))
	switch stem {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(stem) < 4 || stem[:3] != "COM" && stem[:3] != "LPT" {
		return false
	}
	switch stem[3:] {
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
		return true
	}
	return false
}

// legalExt reports whether ext can end a name everywhere Filename promises: no
// forbidden, control or invisible characters, and no trailing space or dot,
// which Windows would strip.
func legalExt(ext string) bool {
	if !utf8.ValidString(ext) || strings.ContainsAny(ext, forbidden) ||
		strings.HasSuffix(ext, ".") || strings.HasSuffix(ext, " ") {
		return false
	}
	return !strings.ContainsFunc(ext, func(r rune) bool { return unicode.IsControl(r) || invisible(r) })
}
