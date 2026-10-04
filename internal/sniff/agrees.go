package sniff

import "strings"

// Agrees reports whether a file extension fits a detected type, so a correct
// name is never "fixed": "jpeg" fits "jpg", "htm" fits "html", "md" fits "txt".
//
// Spellings of one type fit each other, in any case and with or without the dot.
// A type also fits the formats built on it: TIFF fits the camera RAW files that
// are TIFF inside, ZIP fits .docx and .jar, OLE fits .doc and .msi, a generic MP4
// fits .m4a and .mov. Text (txt, json, html, xml, svg) fits every extension
// that does not name a binary format, since text formats are too many to list:
// .vtt, .gradle and go.mod are all correctly named. The few extensions a binary
// format shares with common text files (.key, .pub, .dot) fit text as well.
//
// A compound extension is judged by its last part, so "tar.gz" fits gzip. No
// extension fits only what usually goes without one: text (README, Makefile),
// JSON (.eslintrc) and Unix executables. An all-digit extension is a sequence
// number (archive.7z.001, libc.so.6, perl5.34, app.log.1) and fits archives,
// Unix executables and text. An empty detection fits anything.
func Agrees(ext, detected string) bool {
	if detected == "" {
		return true
	}
	ext, detected = canonical(ext), canonical(detected)
	switch {
	case ext == detected:
		return true
	case ext == "":
		return unnamed[detected]
	case textual[detected]:
		return !claimed[ext]
	case fits[detected][ext]:
		return true
	case digits(ext):
		return numbered[detected]
	}
	return volume(ext, detected)
}

// canonical lowercases an extension, drops a leading dot, keeps the last part of
// a compound extension, and folds an alias into its group's name.
func canonical(s string) string {
	s = strings.ToLower(strings.TrimPrefix(s, "."))
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		s = s[i+1:]
	}
	if a, ok := aliases[s]; ok {
		return a
	}
	return s
}

// aliases fold the spellings of one type into one name, in both directions.
var aliases = map[string]string{
	"jpeg": "jpg", "jpe": "jpg", "jfif": "jpg",
	"tif": "tiff",
	"htm": "html", "xhtml": "html", "shtml": "html",
	"m4v": "mp4",
	"m4b": "m4a", "aac": "m4a",
	"qt":   "mov",
	"midi": "mid",
	"aif":  "aiff",
	"yml":  "yaml",
	"tgz":  "gz",
}

// textual are the text types. Text fits any extension a binary format does not
// claim.
var textual = map[string]bool{"txt": true, "json": true, "html": true, "xml": true, "svg": true}

// unnamed are the types a file without an extension is correctly named as.
var unnamed = map[string]bool{"txt": true, "json": true, "elf": true, "macho": true}

// numbered are the types a sequence-number extension fits: split archive
// volumes, versioned libraries and programs (libc.so.6, perl5.34), and (as text)
// rotated logs.
var numbered = map[string]bool{
	"zip": true, "7z": true, "rar": true, "gz": true, "bz2": true, "xz": true,
	"zst": true, "lz4": true, "tar": true, "elf": true, "macho": true,
}

// families lists, for each detected type, the extensions of formats built on it
// or sold under another name, which fit it as well as its own spellings do.
var families = map[string][]string{
	"jpg":  {"jif", "jfi", "pjpeg", "pjp", "thm", "mpo"},
	"png":  {"apng"},
	"bmp":  {"dib"},
	"tiff": {"dng", "nef", "nrw", "cr2", "arw", "srf", "sr2", "pef", "3fr", "erf", "kdc", "dcr", "mos", "mef", "iiq", "srw", "fff", "orf", "rw2", "gpr", "raw", "ptif", "btf", "tf8"},
	"psd":  {"psb", "pdd"},
	"heic": {"heif", "heics", "heifs", "hif"},
	"heif": {"heic", "heics", "heifs", "hif"},
	"avif": {"avifs"},
	"cr3":  {"crm"},
	"pdf":  {"ai", "ait"},
	"ps":   {"eps", "epsf", "epsi", "epi", "ept", "ai", "ait"},
	"eps":  {"ps", "epsf", "epsi", "epi", "ept", "ai", "ait"},

	"mp4":  {"m4a", "m4p", "mov", "3gp", "3g2", "f4v", "f4a", "f4b", "f4p", "ismv", "isma", "lrv", "insv", "360", "mp4v", "mpeg4"},
	"m4a":  {"m4p", "mp4", "adts"},
	"3gp":  {"3g2", "3gpp", "3gpp2", "3ga"},
	"webm": {"weba", "mkv", "mka"}, // WebM is a Matroska profile
	"mkv":  {"mka", "mks", "mk3d"},
	"avi":  {"divx"},
	"wav":  {"wave", "bwf"},
	"mp3":  {"mp2", "mp1", "mpa", "mpga"},
	"ogg":  {"oga", "ogv", "ogx", "spx"},
	"opus": {"ogg", "oga"},
	"aiff": {"aifc"},
	"mid":  {"kar", "smf"},

	"ttf": {"otf", "tte"}, // an .otf can hold TrueType outlines
	"otf": {"ttf"},
	"ttc": {"otc"},

	"gz":  {"gzip", "svgz", "emz", "wmz", "vgz", "pprof", "pgo", "dia", "graffle", "xopp"},
	"bz2": {"bzip2", "tbz", "tbz2", "tb2"},
	"xz":  {"txz"},
	"zst": {"zstd", "tzst"},
	"7z":  {"cb7"},
	"rar": {"cbr"},
	"tar": {"cbt", "ova", "gem"},
	"zip": {
		"zipx", "jar", "war", "ear", "aar", "apk", "apks", "xapk", "aab", "ipa", "xpi", "kmz",
		"docx", "docm", "dotx", "dotm", "xlsx", "xlsm", "xltx", "xltm", "xlsb", "xlam",
		"pptx", "pptm", "potx", "potm", "ppsx", "ppsm", "ppam", "sldx", "sldm", "thmx",
		"vsdx", "vsdm", "vssx", "vstx", "xps", "oxps", "3mf",
		"odt", "ods", "odp", "odg", "odf", "odc", "odb", "odm", "ott", "ots", "otp", "otg", "oth",
		"sxw", "sxc", "sxi", "sxd", "epub", "pages", "numbers", "key",
		"nupkg", "vsix", "appx", "appxbundle", "msix", "msixbundle", "whl", "cbz",
		"idml", "kra", "ora", "sketch", "usdz", "sb3", "mcworld", "mcpack", "graffle",
	},
	// A .zip name fits a Java or Android archive, whose detection rests on one
	// entry; it does not fit a document, which a reader needs named right.
	"jar":  {"war", "ear", "sar", "par", "nbm", "hpi", "jpi", "xpi", "zip"},
	"apk":  {"aar", "apks", "xapk", "zip"},
	"docx": {"docm", "dotx", "dotm"},
	"xlsx": {"xlsm", "xltx", "xltm", "xlsb", "xlam"},
	"pptx": {"pptm", "potx", "potm", "ppsx", "ppsm", "ppam", "sldx", "sldm", "thmx"},

	"ole": {
		"doc", "dot", "xls", "xlt", "xla", "xlw", "ppt", "pot", "pps", "ppa", "msg", "oft",
		"msi", "msp", "msm", "mst", "vsd", "vss", "vst", "pub", "mpp", "wps", "db", "hwp",
		"sldprt", "sldasm", "slddrw", "max", "cfb",
	},
	"doc": {"dot"},
	"xls": {"xlt", "xla", "xlw"},
	"ppt": {"pot", "pps", "ppa"},
	"msg": {"oft"},

	"sqlite": {"sqlite3", "db", "db3", "s3db", "sl3", "gpkg", "mbtiles", "anki2"},
	"exe": {
		"dll", "sys", "scr", "ocx", "cpl", "efi", "mui", "com", "drv", "ax", "acm", "tsp",
		"fon", "msstyles", "node", "pyd", "xll", "winmd", "mun", "ime", "bpl", "dpl", "rll", "vxd",
	},
	"elf":   {"so", "o", "obj", "ko", "axf", "out", "prx", "puff", "node", "appimage", "syso", "cgi"},
	"macho": {"dylib", "bundle", "o", "so", "node", "syso", "cgi"},
}

// sharedWithText are extensions a binary format shares with common text files.
// A PEM key and a Keynote deck are both .key; an SSH public key and a Publisher
// file are both .pub; a Graphviz graph and a Word template are both .dot; a
// gettext template and a PowerPoint one are both .pot; a CGI program can be a
// script or a binary. Text keeps them.
var sharedWithText = []string{
	"key", "pub", "dot", "pot", "sys", "com", "scr", "msg", "out", "tsp", "obj", "cgi",
	"raw", "dia", "graffle", "pgo",
}

// types lists every type Detect and DetectFile return.
var types = []string{
	"pdf", "png", "jpg", "gif", "webp", "bmp", "tiff", "ico", "psd", "heic", "heif", "avif",
	"mp4", "m4a", "m4v", "mov", "3gp", "cr3", "webm", "mkv", "avi", "flv", "swf",
	"wav", "mp3", "aac", "flac", "ogg", "opus", "aiff", "mid",
	"zip", "gz", "bz2", "xz", "zst", "lz4", "7z", "rar", "tar",
	"docx", "xlsx", "pptx", "odt", "ods", "odp", "odg", "epub", "jar", "apk",
	"ole", "doc", "xls", "ppt", "msg",
	"sqlite", "exe", "elf", "macho", "class", "wasm",
	"rtf", "ps", "eps", "chm", "ttf", "otf", "ttc", "woff", "woff2",
	"txt", "json", "html", "xml", "svg",
}

// fits is families keyed and filled with canonical names, for lookup.
var fits = func() map[string]map[string]bool {
	m := map[string]map[string]bool{}
	for t, exts := range families {
		set := map[string]bool{}
		for _, e := range exts {
			set[canonical(e)] = true
		}
		m[canonical(t)] = set
	}
	return m
}()

// claimed are the extensions that name a binary format: every type's own name
// and every extension in its family, less the text types, sharedWithText, and
// sequence numbers such as GoPro's .360, which rotated logs share.
var claimed = func() map[string]bool {
	m := map[string]bool{}
	for _, t := range types {
		m[canonical(t)] = true
	}
	for t, exts := range families {
		m[canonical(t)] = true
		for _, e := range exts {
			if !digits(e) {
				m[canonical(e)] = true
			}
		}
	}
	for t := range textual {
		delete(m, t)
	}
	for _, e := range sharedWithText {
		delete(m, canonical(e))
	}
	return m
}()

// volume reports whether ext numbers a later part of a split archive: RAR names
// them r00, r01 and on, ZIP z01, z02 and on.
func volume(ext, detected string) bool {
	if len(ext) != 3 || !digits(ext[1:]) {
		return false
	}
	return (ext[0] == 'r' && detected == "rar") || (ext[0] == 'z' && detected == "zip")
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
