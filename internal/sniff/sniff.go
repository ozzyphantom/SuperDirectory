// Package sniff tells a file's type from its content.
//
// A file's name says what it should be; its first bytes say what it is. A PDF
// renamed .txt is still a PDF, and a reader that trusts the name fails on it.
// Detect names the type the leading bytes show. DetectFile also looks inside the
// containers a head cannot settle: a .docx and a .jar are both ZIP archives, a
// .doc and a .msg both OLE2 compound files. Agrees reports whether an extension
// already fits, so a correct name is left alone.
//
// Types are lowercase extensions without the dot, the way package organize names
// its folders: "pdf", "jpg", "docx". Three are families rather than extensions a
// file carries: "ole" is a compound file DetectFile could not place, and "elf"
// and "macho" are executables, which on their own systems carry no extension.
//
// The input is untrusted. Every offset is bounds-checked, every read is capped,
// and a malformed file yields a type or "", never a panic.
package sniff

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

// HeadSize is how many leading bytes Detect wants. Most signatures sit in the
// first few bytes, but a tar header ends at byte 512, two MPEG audio frames can
// span 3 KiB, and an SVG's root element can follow a long prolog.
const HeadSize = 4096

// Detect names the type the leading bytes show, as a lowercase extension without
// the dot ("pdf", "jpg", "txt"), or "" when it cannot tell.
//
// It looks at no more than HeadSize bytes, and takes a shorter head to be the
// whole file. Only a whole file can be called JSON: a document cut short does
// not parse.
func Detect(head []byte) string {
	if len(head) > HeadSize {
		head = head[:HeadSize]
	}
	return detect(head, len(head) < HeadSize)
}

// detect is Detect, told whether head is the whole file.
func detect(head []byte, whole bool) string {
	for _, check := range checks {
		if t := check(head); t != "" {
			return t
		}
	}
	if t := text(head, whole); t != "" {
		return t
	}
	// MPEG audio and raw AAC (ADTS) have no magic number, only a frame sync that
	// other bytes can imitate. So they come after text, and need two frames in a
	// row to agree.
	switch {
	case mpegAudio(head):
		return "mp3"
	case adts(head):
		return "aac"
	}
	return ""
}

// DetectFile is Detect, refined for containers that need more than the head:
// ZIP-based formats by their entries, OLE2 compound files by their streams.
//
// Beyond the head it reads at most a ZIP's last 64 KiB, its central directory
// (capped at 8 MiB) and 1 KiB of a "mimetype" entry; a compound file's header,
// 4096 directory entries and the FAT sectors that chain them; or 16 bytes behind
// an ID3 tag longer than the head. A container too damaged to read further keeps
// the type its head showed. The error reports a failed read; a malformed or
// truncated file is not an error.
func DetectFile(r io.ReaderAt, size int64) (string, error) {
	if size < 0 {
		return "", errors.New("sniff: negative size")
	}
	head := make([]byte, min(size, HeadSize))
	if n, err := r.ReadAt(head, 0); n < len(head) {
		if err != nil && !isShort(err) {
			return "", err
		}
		head, size = head[:n], int64(n) // the file is shorter than its size said
	}
	t := detect(head, int64(len(head)) == size)
	var err error
	switch t {
	case "zip":
		t, err = zipType(r, size)
	case "ole":
		t, err = oleType(r, size)
	case "mp3":
		t, err = id3Type(r, size, head)
	}
	if err != nil {
		return "", err
	}
	return t, nil
}

// errShort is a read that would run past the end of the file: the container's
// offsets lie, or it was cut off. That is damage, not a failed read.
var errShort = errors.New("sniff: read past the end of the file")

// readAt fills b from offset off of a file size bytes long, refusing a read that
// would leave the file.
func readAt(r io.ReaderAt, size int64, b []byte, off int64) error {
	if off < 0 || off > size || int64(len(b)) > size-off {
		return errShort
	}
	n, err := r.ReadAt(b, off)
	if n == len(b) {
		return nil
	}
	if err == nil || isShort(err) {
		return errShort
	}
	return err
}

func isShort(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// failed is a container's verdict when a read stops: the head's type t when the
// container is damaged, the error when the read itself failed.
func failed(t string, err error) (string, error) {
	if errors.Is(err, errShort) {
		return t, nil
	}
	return "", err
}

// checks run in order, and the first to name a type wins. Most test a magic
// number at offset 0. Where a magic number is short enough to begin an ordinary
// file ("BM", "MZ", "true"), the check also validates the header after it.
var checks = []func(b []byte) string{
	prefix, riff, isoBMFF, ebml, id3, flac, ogg, bmp, ico, psd, ustar, mz, machO,
	postScript, font, swf, flv, chm,
}

// signatures are magic numbers that settle a type alone: long enough, or odd
// enough, that no ordinary file starts with them.
var signatures = []struct{ magic, typ string }{
	{"%PDF-", "pdf"},
	{"\x89PNG\r\n\x1a\n", "png"},
	{"\xff\xd8\xff", "jpg"},
	{"GIF87a", "gif"},
	{"GIF89a", "gif"},
	{"II*\x00", "tiff"},
	{"MM\x00*", "tiff"},
	{"II+\x00\x08\x00\x00\x00", "tiff"}, // BigTIFF: 64-bit offsets
	{"MM\x00+\x00\x08\x00\x00", "tiff"},
	{"MThd\x00\x00\x00\x06", "mid"},
	{"PK\x03\x04", "zip"},
	{"PK\x05\x06", "zip"},           // an empty archive: the end record alone
	{"PK\x07\x08PK\x03\x04", "zip"}, // the marker of a split archive's first part
	{"PK00PK\x03\x04", "zip"},
	{"\xfd7zXZ\x00", "xz"},
	{"\x28\xb5\x2f\xfd", "zst"},
	{"\x04\x22\x4d\x18", "lz4"},
	{"7z\xbc\xaf\x27\x1c", "7z"},
	{"Rar!\x1a\x07\x00", "rar"},     // RAR 1.5 to 4
	{"Rar!\x1a\x07\x01\x00", "rar"}, // RAR 5
	{"SQLite format 3\x00", "sqlite"},
	{"\x00asm\x01\x00\x00\x00", "wasm"},
	{"{\\rtf", "rtf"},
	{"\xc5\xd0\xd3\xc6", "eps"}, // DOS EPS: a binary preamble before the PostScript
	{"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1", "ole"},
}

// prefix matches the signatures, and the few magic numbers that need a byte or
// two after them checked.
func prefix(b []byte) string {
	for _, s := range signatures {
		if hasPrefix(b, s.magic) {
			return s.typ
		}
	}
	switch {
	case len(b) >= 4 && hasPrefix(b, "\x1f\x8b\x08") && b[3]&0xe0 == 0:
		return "gz" // deflate, with none of the reserved flags set
	case len(b) >= 10 && hasPrefix(b, "BZh") && b[3] >= '1' && b[3] <= '9' &&
		(string(b[4:10]) == "1AY&SY" || string(b[4:10]) == "\x17\x72\x45\x38\x50\x90"):
		return "bz2" // a block size, then a block or the end-of-stream marker
	case len(b) >= 7 && hasPrefix(b, "\x7fELF") && (b[4] == 1 || b[4] == 2) && (b[5] == 1 || b[5] == 2) && b[6] == 1:
		return "elf" // 32 or 64 bits, either byte order, version 1
	}
	return ""
}

// riff names the RIFF formats by their form type: WebP, WAV and AVI share the
// container, and RF64 and BW64 are WAV past 4 GiB. AIFF uses IFF, RIFF's
// big-endian ancestor, with the same layout.
func riff(b []byte) string {
	if len(b) < 12 {
		return ""
	}
	form := string(b[8:12])
	switch string(b[:4]) {
	case "RIFF":
		switch form {
		case "WEBP":
			return "webp"
		case "WAVE":
			return "wav"
		case "AVI ":
			return "avi"
		}
	case "RF64", "BW64":
		if form == "WAVE" {
			return "wav"
		}
	case "FORM":
		if form == "AIFF" || form == "AIFC" {
			return "aiff"
		}
	}
	return ""
}

// bmp validates what follows "BM", which text can start with: an info header of
// one of the few sizes that exist, and a single color plane.
func bmp(b []byte) string {
	if len(b) < 28 || string(b[:2]) != "BM" {
		return ""
	}
	planes := binary.LittleEndian.Uint16(b[26:28])
	switch binary.LittleEndian.Uint32(b[14:18]) {
	case 12: // OS/2 1.x: 16-bit width and height put the planes earlier
		planes = binary.LittleEndian.Uint16(b[22:24])
	case 16, 40, 52, 56, 64, 108, 124:
	default:
		return ""
	}
	if planes != 1 {
		return ""
	}
	return "bmp"
}

// ico validates an icon directory, whose magic is mostly zeros: at least one
// image, and a first entry with a real bit depth and its data after the
// directory.
func ico(b []byte) string {
	if len(b) < 22 || string(b[:4]) != "\x00\x00\x01\x00" {
		return ""
	}
	n := uint32(binary.LittleEndian.Uint16(b[4:6]))
	e := b[6:22]
	switch binary.LittleEndian.Uint16(e[6:8]) {
	case 0, 1, 4, 8, 16, 24, 32:
	default:
		return ""
	}
	if n == 0 || (e[3] != 0 && e[3] != 0xff) || binary.LittleEndian.Uint16(e[4:6]) > 1 ||
		binary.LittleEndian.Uint32(e[8:12]) == 0 || binary.LittleEndian.Uint32(e[12:16]) < 6+16*n {
		return ""
	}
	return "ico"
}

// psd checks Photoshop's version, 1 or 2 for a large document, and the six
// reserved zero bytes after it.
func psd(b []byte) string {
	if len(b) < 12 || string(b[:4]) != "8BPS" {
		return ""
	}
	if v := binary.BigEndian.Uint16(b[4:6]); (v != 1 && v != 2) || string(b[6:12]) != "\x00\x00\x00\x00\x00\x00" {
		return ""
	}
	return "psd"
}

// ustar recognizes a POSIX or GNU tar header by "ustar" at offset 257, and
// confirms it with the header's checksum. Old V7 archives carry no magic and go
// unnamed.
func ustar(b []byte) string {
	if len(b) < 512 || string(b[257:262]) != "ustar" || (b[262] != 0 && b[262] != ' ') {
		return ""
	}
	want, ok := octal(b[148:156])
	if !ok {
		return ""
	}
	// The checksum adds up the header's bytes, its own field counted as spaces.
	// Some old writers summed signed bytes, so either sum is accepted.
	var unsigned, signed int64
	for i, c := range b[:512] {
		if i >= 148 && i < 156 {
			c = ' '
		}
		unsigned += int64(c)
		signed += int64(int8(c))
	}
	if want != unsigned && want != signed {
		return ""
	}
	return "tar"
}

// octal parses a tar number field: octal digits, padded with spaces or NULs.
func octal(b []byte) (int64, bool) {
	b = bytes.Trim(b, " \x00")
	if len(b) == 0 {
		return 0, false
	}
	var v int64
	for _, c := range b {
		if c < '0' || c > '7' {
			return 0, false
		}
		v = v<<3 | int64(c-'0')
	}
	return v, true
}

// mz validates what follows "MZ", which text can start with: a PE signature
// where the DOS header points, or else a DOS header that adds up, with a last
// page of under 512 bytes and at least one page. Text cannot pass either test.
func mz(b []byte) string {
	if len(b) < 64 || string(b[:2]) != "MZ" {
		return ""
	}
	if pe := int64(binary.LittleEndian.Uint32(b[0x3c:0x40])); pe >= 64 && pe+4 <= int64(len(b)) && string(b[pe:pe+4]) == "PE\x00\x00" {
		return "exe"
	}
	if binary.LittleEndian.Uint16(b[2:4]) < 512 && binary.LittleEndian.Uint16(b[4:6]) > 0 {
		return "exe"
	}
	return ""
}

// machO names Mach-O files: thin ones in either byte order and of 32 or 64 bits,
// and fat (universal) ones. A fat binary starts 0xCAFEBABE, as a Java class file
// does. The next word tells them apart: a fat binary counts its architectures
// there, a handful at most, and a class file keeps its version, 45 or more.
func machO(b []byte) string {
	if len(b) < 8 {
		return ""
	}
	switch m := binary.BigEndian.Uint32(b); m {
	case 0xfeedface, 0xfeedfacf, 0xcefaedfe, 0xcffaedfe:
		return "macho"
	case 0xcafebabe, 0xcafebabf:
		if n := binary.BigEndian.Uint32(b[4:8]); n >= 1 && n <= 32 {
			return "macho"
		}
		if m == 0xcafebabe && binary.BigEndian.Uint16(b[6:8]) >= 45 {
			return "class"
		}
	}
	return ""
}

// postScript tells EPS from plain PostScript by the first line, where an EPS
// file declares itself ("%!PS-Adobe-3.0 EPSF-3.0"). Some Windows drivers put a
// Ctrl-D in front.
func postScript(b []byte) string {
	b = bytes.TrimPrefix(b, []byte{0x04})
	if !hasPrefix(b, "%!PS") {
		return ""
	}
	line := b[:min(len(b), 256)]
	if i := bytes.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	if bytes.Contains(line, []byte("EPSF")) {
		return "eps"
	}
	return "ps"
}

// font names OpenType fonts and their wrappers. A bare font starts with a
// version tag ("true", "OTTO", or 0x00010000) that text can imitate, so its table
// directory is checked too.
func font(b []byte) string {
	if len(b) < 16 {
		return ""
	}
	switch string(b[:4]) {
	case "\x00\x01\x00\x00", "true":
		if sfnt(b) {
			return "ttf"
		}
	case "OTTO":
		if sfnt(b) {
			return "otf"
		}
	case "ttcf":
		if v := binary.BigEndian.Uint32(b[4:8]); (v == 0x00010000 || v == 0x00020000) && binary.BigEndian.Uint32(b[8:12]) > 0 {
			return "ttc"
		}
	case "wOFF", "wOF2":
		// The header names the flavor of the font inside, and keeps a zero.
		switch string(b[4:8]) {
		case "\x00\x01\x00\x00", "true", "OTTO", "ttcf":
		default:
			return ""
		}
		if binary.BigEndian.Uint16(b[12:14]) == 0 || binary.BigEndian.Uint16(b[14:16]) != 0 {
			return ""
		}
		if b[3] == 'F' {
			return "woff"
		}
		return "woff2"
	}
	return ""
}

// sfnt checks an OpenType table directory: one to 512 tables, and a tag of four
// printable ASCII characters on every record the head holds.
func sfnt(b []byte) bool {
	n := int(binary.BigEndian.Uint16(b[4:6]))
	if n == 0 || n > 512 || len(b) < 28 {
		return false
	}
	for i := 0; i < n && 12+16*i+16 <= len(b); i++ {
		for _, c := range b[12+16*i : 12+16*i+4] {
			if c < 0x20 || c > 0x7e {
				return false
			}
		}
	}
	return true
}

// swf validates the header after "FWS", "CWS" or "ZWS" (uncompressed, zlib,
// LZMA): a version from 1 to 64, and a length under 256 MiB, which no run of text
// bytes can spell.
func swf(b []byte) string {
	if len(b) < 8 {
		return ""
	}
	switch string(b[:3]) {
	case "FWS", "CWS", "ZWS":
	default:
		return ""
	}
	if n := binary.LittleEndian.Uint32(b[4:8]); b[3] == 0 || b[3] > 64 || n < 8 || n >= 1<<28 {
		return ""
	}
	return "swf"
}

// flv validates the header after "FLV": version 1, no flags but audio and video,
// and the 9-byte header length.
func flv(b []byte) string {
	if len(b) < 9 || string(b[:4]) != "FLV\x01" || b[4]&^0x05 != 0 || binary.BigEndian.Uint32(b[5:9]) != 9 {
		return ""
	}
	return "flv"
}

// chm checks the version after "ITSF": 2 or 3.
func chm(b []byte) string {
	if len(b) < 8 || string(b[:4]) != "ITSF" {
		return ""
	}
	if v := binary.LittleEndian.Uint32(b[4:8]); v != 2 && v != 3 {
		return ""
	}
	return "chm"
}

func hasPrefix(b []byte, s string) bool {
	return len(b) >= len(s) && string(b[:len(s)]) == s
}

// from returns b from offset off, or nil when off is past its end.
func from(b []byte, off int) []byte {
	if off < 0 || off > len(b) {
		return nil
	}
	return b[off:]
}
