package sniff

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// fixture is a small file of a known type, built in code.
type fixture struct {
	name   string
	data   []byte
	detect string // what Detect says of its head
	file   string // what DetectFile says of the whole file
}

// fixtures returns a file of every type the package names, and of the variants
// that reach a type by different paths.
func fixtures(tb testing.TB) []fixture {
	tb.Helper()
	same := func(name, typ string, data []byte) fixture { return fixture{name, data, typ, typ} }
	inside := func(name, outer, typ string, data []byte) fixture { return fixture{name, data, outer, typ} }
	return []fixture{
		same("pdf", "pdf", join("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n1 0 obj\n<< /Type /Catalog >>\nendobj\n")),
		same("png", "png", encoded(tb, func(b *bytes.Buffer, m image.Image) error { return png.Encode(b, m) })),
		same("jpg", "jpg", encoded(tb, func(b *bytes.Buffer, m image.Image) error { return jpeg.Encode(b, m, nil) })),
		same("gif", "gif", encoded(tb, func(b *bytes.Buffer, m image.Image) error { return gif.Encode(b, m, nil) })),
		same("webp", "webp", riffFile("WEBP", join("VP8L", le32(5), "\x2f\x00\x00\x00\x00", 0))),
		same("bmp", "bmp", bmpFile(40)),
		same("bmp os2", "bmp", bmpFile(12)),
		same("tiff little-endian", "tiff", join("II*\x00", le32(8), le16(1), le16(256), le16(3), le32(1), le16(1), le16(0), le32(0))),
		same("tiff big-endian", "tiff", join("MM\x00*", be32(8), be16(1), be16(256), be16(3), be32(1), be16(1), be16(0), be32(0))),
		same("bigtiff", "tiff", join("II+\x00", le16(8), le16(0), le64(16), le64(0))),
		same("ico", "ico", join("\x00\x00\x01\x00", le16(1), 16, 16, 0, 0, le16(1), le16(32), le32(40), le32(22), zeros(40))),
		same("psd", "psd", psdFile(1)),
		same("psb", "psd", psdFile(2)),

		same("heic", "heic", isoFile("heic", "mif1", "heic")),
		same("heif refined by heic", "heic", isoFile("mif1", "mif1", "heic", "miaf")),
		same("heif", "heif", isoFile("mif1", "mif1", "miaf", "MiHB")),
		same("avif", "avif", isoFile("avif", "avif", "mif1", "miaf", "MA1B")),
		same("avif under mif1", "avif", isoFile("mif1", "avif", "mif1", "miaf")),
		same("mp4", "mp4", isoFile("isom", "isom", "iso2", "avc1", "mp41")),
		same("mp4 listing qt", "mp4", isoFile("mp42", "mp42", "isom", "qt  ")),
		same("mp4 listing 3gp", "mp4", isoFile("isom", "isom", "3gp4")),
		same("m4a", "m4a", isoFile("M4A ", "M4A ", "mp42", "isom")),
		same("m4a under mp42", "m4a", isoFile("mp42", "M4A ", "mp42", "isom")),
		same("m4b", "m4a", isoFile("M4B ", "M4B ", "mp42", "isom")),
		same("m4v", "m4v", isoFile("M4V ", "M4V ", "M4A ", "mp42", "isom")),
		same("m4v under mp42", "m4v", isoFile("mp42", "M4A ", "M4V ", "mp42", "isom")),
		same("mov", "mov", isoFile("qt  ", "qt  ")),
		same("3gp", "3gp", isoFile("3gp4", "isom", "3gp4")),
		same("3g2", "3gp", isoFile("3g2a", "3g2a")),
		same("cr3", "cr3", isoFile("crx ", "crx ", "isom")),

		same("webm", "webm", ebmlFile("webm")),
		same("mkv", "mkv", ebmlFile("matroska")),
		same("avi", "avi", riffFile("AVI ", join("LIST", le32(4), "hdrl"))),
		same("wav", "wav", riffFile("WAVE", join("fmt ", le32(16), le16(1), le16(2), le32(44100), le32(176400), le16(4), le16(16), "data", le32(0)))),
		same("rf64", "wav", join("RF64", le32(0xffffffff), "WAVE", "ds64", le32(28), zeros(28))),
		same("flv", "flv", join("FLV\x01", 5, be32(9), be32(0))),
		same("swf", "swf", swfFile(tb, "FWS")),
		same("swf zlib", "swf", swfFile(tb, "CWS")),

		same("mp3 frames", "mp3", mpegFrames(3)),
		same("mp3 after id3", "mp3", join(id3Tag(64), mpegFrames(2))),
		same("flac after id3", "flac", join(id3Tag(64), flacFile())),
		same("aac after id3", "aac", join(id3Tag(64), adtsFrames(2))),
		same("mp3 after a long id3", "mp3", join(id3Tag(6000), mpegFrames(2))),
		inside("flac after a long id3", "mp3", "flac", join(id3Tag(6000), flacFile())),
		inside("aac after a long id3", "mp3", "aac", join(id3Tag(6000), adtsFrames(2))),
		same("aac", "aac", adtsFrames(3)),
		same("flac", "flac", flacFile()),
		same("ogg vorbis", "ogg", oggPage(join("\x01vorbis", le32(0), 2, le32(44100), le32(0), le32(128000), le32(0), 0xb8, 1))),
		same("opus", "opus", oggPage(join("OpusHead", 1, 2, le16(312), le32(48000), le16(0), 0))),
		same("aiff", "aiff", aiffFile("AIFF")),
		same("aifc", "aiff", aiffFile("AIFC")),
		same("midi", "mid", join("MThd", be32(6), be16(0), be16(1), be16(96), "MTrk", be32(4), "\x00\xff\x2f\x00")),

		same("zip", "zip", zipFile(tb, zipEntry{name: "notes.txt", body: "hello"})),
		same("empty zip", "zip", zipFile(tb)),
		inside("docx", "zip", "docx", zipFile(tb, ooxml("word/document.xml")...)),
		inside("xlsx", "zip", "xlsx", zipFile(tb, ooxml("xl/workbook.xml")...)),
		inside("pptx", "zip", "pptx", zipFile(tb, ooxml("ppt/presentation.xml")...)),
		inside("odt", "zip", "odt", zipFile(tb, odf("application/vnd.oasis.opendocument.text")...)),
		inside("ods", "zip", "ods", zipFile(tb, odf("application/vnd.oasis.opendocument.spreadsheet")...)),
		inside("odp", "zip", "odp", zipFile(tb, odf("application/vnd.oasis.opendocument.presentation")...)),
		inside("odg", "zip", "odg", zipFile(tb, odf("application/vnd.oasis.opendocument.graphics")...)),
		inside("epub", "zip", "epub", zipFile(tb, epub()...)),
		inside("jar", "zip", "jar", zipFile(tb, zipEntry{name: "META-INF/MANIFEST.MF", body: "Manifest-Version: 1.0\r\n"}, zipEntry{name: "com/example/Main.class", body: "\xca\xfe\xba\xbe"})),
		inside("apk", "zip", "apk", zipFile(tb,
			zipEntry{name: "AndroidManifest.xml", body: "\x03\x00\x08\x00"},
			zipEntry{name: "classes.dex", body: "dex\n035\x00"},
			zipEntry{name: "META-INF/MANIFEST.MF", body: "Manifest-Version: 1.0\r\n"})),
		same("gz", "gz", gzipFile(tb)),
		same("bz2", "bz2", unhex(tb, "425a6839314159265359b2066117000003518000104004036588002000220681908069a68a0706d9e2a178bb9229c28485903308b8")),
		same("bz2 empty stream", "bz2", join("BZh9", "\x17\x72\x45\x38\x50\x90", le32(0))),
		same("xz", "xz", unhex(tb, "fd377a585a000004e6d6b44604c0110d2101160000000000000000008888cd6801000c68656c6c6f2c20736e6966660a0000000028412590ebcd6b9a00012d0d79931d7e1fb6f37d010000000004595a")),
		same("zst", "zst", unhex(tb, "28b52ffd240d69000068656c6c6f2c20736e6966660a422a01f6")),
		same("lz4", "lz4", unhex(tb, "04224d186440a70d00008068656c6c6f2c20736e6966660a00000000a3487ed1")),
		same("7z", "7z", unhex(tb, "377abcaf271c0004563a3f5d11000000000000005a000000000000001101a89d01000c68656c6c6f2c20736e6966660a000104060001091100070b010001212101000c0d00080a013f286cea00000501190c000000000000000000000000111500680065006c006c006f002e007400780074000000140a010008a6c1ba9053dd01150601002080a4810000")),
		same("rar4", "rar", join("Rar!\x1a\x07\x00", "\xcf\x90\x73\x00\x00\x0d\x00\x00\x00\x00\x00\x00\x00")),
		same("rar5", "rar", join("Rar!\x1a\x07\x01\x00", "\x33\x92\xb5\xe5\x0a\x01\x05\x06\x00\x05\x01\x01\x80\x80\x00")),
		same("tar", "tar", tarFile(tb, tar.FormatUSTAR)),
		same("gnu tar", "tar", tarFile(tb, tar.FormatGNU)),
		same("sqlite", "sqlite", unhex(tb, "53514c69746520666f726d61742033001000010100402020000000020000000200000000000000000000000100000004000000000000000000000001000000000000000000000000000000000000000000000000000000000000000000000002002e95ca")),

		inside("doc", "ole", "doc", oleFile(9, []oleEntry{
			rootEntry(2), stream("1Table", none, none), stream("WordDocument", 1, 3), stream("\x05SummaryInformation", none, none),
		})),
		inside("xls", "ole", "xls", oleFile(9, chain("\x05SummaryInformation", "Workbook"))),
		inside("xls from Excel 5", "ole", "xls", oleFile(9, chain("Book"))),
		inside("ppt", "ole", "ppt", oleFile(9, chain("Current User", "PowerPoint Document", "Pictures"))),
		inside("msg", "ole", "msg", oleFile(9, []oleEntry{
			rootEntry(1), storage("__nameid_version1.0", none, 2, none), stream("__substg1.0_0037001F", none, 3), stream("__properties_version1.0", none, none),
		})),
		inside("doc with 4 KiB sectors", "ole", "doc", oleFile(12, chain("Data", "1Table", "WordDocument"))),
		same("ole with no known stream", "ole", oleFile(9, chain("\x01CompObj", "Contents"))),

		same("exe", "exe", peFile()),
		same("dos exe", "exe", join("MZ", le16(0x1e), le16(1), le16(0), le16(2), le16(0), le16(0xffff), le16(0), le16(0x100), zeros(0x3c-0x12), le32(0), "\xb4\x4c\xcd\x21")),
		same("elf", "elf", join("\x7fELF", 2, 1, 1, 0, zeros(8), le16(2), le16(0x3e), le32(1), zeros(48))),
		same("macho 64 little-endian", "macho", join("\xcf\xfa\xed\xfe", le32(0x0100000c), le32(0), le32(2), zeros(16))),
		same("macho 32 little-endian", "macho", join("\xce\xfa\xed\xfe", le32(7), le32(3), le32(2), zeros(16))),
		same("macho 32 big-endian", "macho", join("\xfe\xed\xfa\xce", be32(18), be32(0), be32(2), zeros(16))),
		same("macho 64 big-endian", "macho", join("\xfe\xed\xfa\xcf", be32(0x01000012), be32(0), be32(2), zeros(16))),
		same("macho fat", "macho", join("\xca\xfe\xba\xbe", be32(2),
			be32(0x01000007), be32(3), be32(0x4000), be32(0x1000), be32(14),
			be32(0x0100000c), be32(0), be32(0x8000), be32(0x1000), be32(14))),
		same("macho fat64", "macho", join("\xca\xfe\xba\xbf", be32(1), be32(0x0100000c), be32(0), zeros(24))),
		same("java class", "class", join("\xca\xfe\xba\xbe", be16(0), be16(65), be16(15), "\x0a\x00\x02\x00\x03")),
		same("java 1.1 class", "class", join("\xca\xfe\xba\xbe", be16(3), be16(45), be16(15), "\x0a\x00\x02\x00\x03")),
		same("wasm", "wasm", join("\x00asm\x01\x00\x00\x00", "\x01\x04\x01\x60\x00\x00")),

		same("rtf", "rtf", join(`{\rtf1\ansi\deff0{\fonttbl{\f0 Helvetica;}}\f0 Hello.\par}`)),
		same("ps", "ps", join("%!PS-Adobe-3.0\n%%Creator: sniff\n%%Pages: 1\n%%EndComments\nshowpage\n")),
		same("ps after ctrl-d", "ps", join("\x04%!PS-Adobe-2.0\n%%EndComments\n")),
		same("eps", "eps", join("%!PS-Adobe-3.0 EPSF-3.0\n%%BoundingBox: 0 0 100 100\n")),
		same("dos eps", "eps", join("\xc5\xd0\xd3\xc6", le32(32), le32(40), le32(0), le32(0), le32(0), le32(0), le16(0xffff), le16(0), "%!PS-Adobe-3.0 EPSF-3.0\n")),
		same("chm", "chm", join("ITSF", le32(3), le32(0x60), le32(1), le32(0x12345678), le32(0x0409), zeros(32))),
		same("ttf", "ttf", sfntFile("\x00\x01\x00\x00")),
		same("ttf from a mac", "ttf", sfntFile("true")),
		same("otf", "otf", sfntFile("OTTO")),
		same("ttc", "ttc", join("ttcf", be32(0x00010000), be32(2), be32(20), be32(120), zeros(16))),
		same("woff", "woff", join("wOFF", "\x00\x01\x00\x00", be32(44), be16(2), be16(0), be32(256), be16(1), be16(0), zeros(20))),
		same("woff2", "woff2", join("wOF2", "OTTO", be32(48), be16(2), be16(0), be32(256), be32(32), be16(1), be16(0), zeros(20))),

		same("svg", "svg", join(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10"/></svg>`)),
		same("svg with prolog", "svg", join(`<?xml version="1.0" encoding="UTF-8" standalone="no"?>`+"\n"+
			`<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd">`+"\n"+
			`<svg version="1.1" xmlns="http://www.w3.org/2000/svg"/>`)),
		same("svg from illustrator", "svg", join(`<?xml version="1.0" encoding="utf-8"?>`+"\n"+
			`<!-- Generator: Adobe Illustrator 27.0.0, SVG Export Plug-In . SVG Version: 6.00 Build 0)  -->`+"\n"+
			`<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd" [`+"\n"+
			"\t"+`<!ENTITY ns_svg "http://www.w3.org/2000/svg">`+"\n"+
			"\t"+`<!-- the entities don't close the doctype > -->`+"\n"+
			`]>`+"\n"+
			`<svg version="1.1" xmlns="&ns_svg;" viewBox="0 0 10 10"/>`)),
		same("html5", "html", join("<!DOCTYPE html>\n<html lang=\"en\"><head><title>t</title></head><body></body></html>\n")),
		same("html 4", "html", join(`<!DOCTYPE HTML PUBLIC "-//W3C//DTD HTML 4.01//EN" "http://www.w3.org/TR/html4/strict.dtd">`+"\n<HTML><BODY>x</BODY></HTML>")),
		same("html element", "html", join("<html><body>hi</body></html>")),
		same("html head", "html", join("<HEAD><TITLE>x</TITLE></HEAD>")),
		same("html body", "html", join("<body bgcolor=white>\n<p>hi\n")),
		same("html title", "html", join("<title>Release notes</title>\n<p>hi")),
		same("html after bom and space", "html", join("\xef\xbb\xbf\r\n  \t<!doctype html>")),
		same("html saved by internet explorer", "html", join("<!-- saved from url=(0014)about:internet -->\r\n<html>\r\n<head>")),
		same("html in utf-16", "html", utf16Text(binary.LittleEndian, "<!DOCTYPE html><html></html>")),
		same("xml", "xml", join(`<?xml version="1.0"?>`+"\n"+`<rss version="2.0"><channel><title>t</title></channel></rss>`)),
		same("xhtml", "xml", join(`<?xml version="1.0" encoding="UTF-8"?>`+"\n"+`<!DOCTYPE html>`+"\n"+`<html xmlns="http://www.w3.org/1999/xhtml"/>`)),
		same("xml in utf-16", "xml", utf16Text(binary.BigEndian, `<?xml version="1.0" encoding="UTF-16"?><plist/>`)),
		same("json", "json", join(`{"name": "sniff", "tags": ["a", "b"], "n": 1.5e3, "ok": true, "none": null}`)),
		same("json array", "json", join("\n  [1, 2, {\"a\": []}]\n")),

		same("text", "txt", join("Release notes\n\n- Tells a file's type from its content.\n")),
		same("text in utf-8", "txt", join("Crème brûlée, 東京, emoji 🎉, and a non-breaking space.\n")),
		same("text with bom", "txt", join("\xef\xbb\xbfNotes saved by Notepad.\r\n")),
		same("text in utf-16le", "txt", utf16Text(binary.LittleEndian, "Hello, 世界 🎉\r\n")),
		same("text in utf-16be", "txt", utf16Text(binary.BigEndian, "Hello, 世界 🎉\r\n")),
		same("text in utf-32le", "txt", utf32Text(binary.LittleEndian, "Hello, 世界 🎉\n")),
		same("text in utf-32be", "txt", utf32Text(binary.BigEndian, "Hello, 世界 🎉\n")),
		same("log with color codes", "txt", join("\x1b[32mok\x1b[0m   build\n\x1b[31mFAIL\x1b[0m test\n\x07done\n")),
		same("markdown with a comment", "txt", join("<!-- markdownlint-disable -->\n# Title\n\nBody.\n")),
		same("xml without a prolog", "txt", join(`<project><modelVersion>4.0.0</modelVersion></project>`)),
		same("json scalar", "txt", join("42\n")),
		same("pdf named in text", "txt", join("Notes on the format.\nEvery file starts with %PDF-1.7 and ends with %%EOF.\n")),
	}
}

// join concatenates strings, byte slices and single bytes (written as small
// integers), so a header can be written field by field.
func join(parts ...any) []byte {
	var out []byte
	for _, p := range parts {
		switch p := p.(type) {
		case string:
			out = append(out, p...)
		case []byte:
			out = append(out, p...)
		case int:
			if p < 0 || p > 255 {
				panic(fmt.Sprintf("join: %d is not a byte", p))
			}
			out = append(out, byte(p))
		default:
			panic(fmt.Sprintf("join: cannot join a %T", p))
		}
	}
	return out
}

func le16(v uint16) []byte { return binary.LittleEndian.AppendUint16(nil, v) }
func le32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }
func le64(v uint64) []byte { return binary.LittleEndian.AppendUint64(nil, v) }
func be16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }
func be32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func zeros(n int) []byte   { return make([]byte, n) }

func unhex(tb testing.TB, s string) []byte {
	tb.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

// encoded draws a small picture and encodes it with enc.
func encoded(tb testing.TB, enc func(*bytes.Buffer, image.Image) error) []byte {
	tb.Helper()
	m := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := range m.Pix {
		m.Pix[i] = byte(i * 37)
	}
	var b bytes.Buffer
	if err := enc(&b, m); err != nil {
		tb.Fatal(err)
	}
	return b.Bytes()
}

// riffFile wraps chunks in a RIFF container of the given form type.
func riffFile(form string, chunks []byte) []byte {
	return join("RIFF", le32(uint32(4+len(chunks))), form, chunks)
}

// bmpFile returns a one-pixel BMP with an info header of the given size: 40 for
// Windows, 12 for OS/2 1.x.
func bmpFile(info uint32) []byte {
	var header []byte
	if info == 12 {
		header = join(le32(12), le16(1), le16(1), le16(1), le16(24))
	} else {
		header = join(le32(info), le32(1), le32(1), le16(1), le16(24), le32(0), le32(4), le32(2835), le32(2835), le32(0), le32(0))
	}
	offset := uint32(14 + len(header))
	return join("BM", le32(offset+4), le32(0), le32(offset), header, "\x00\x00\xff\x00")
}

func psdFile(version uint16) []byte {
	return join("8BPS", be16(version), zeros(6), be16(3), be32(4), be32(4), be16(8), be16(3), be32(0))
}

// isoFile returns an ftyp box with a major brand and compatible brands, then an
// empty media data box.
func isoFile(major string, compat ...string) []byte {
	return join(be32(uint32(16+4*len(compat))), "ftyp", major, be32(0), strings.Join(compat, ""), be32(8), "mdat")
}

// ebmlFile returns an EBML header declaring docType, then the start of a segment
// of unknown size.
func ebmlFile(docType string) []byte {
	body := join(
		"\x42\x86\x81\x01", // EBMLVersion 1
		"\x42\xf7\x81\x01", // EBMLReadVersion 1
		"\x42\xf2\x81\x04", // EBMLMaxIDLength 4
		"\x42\xf3\x81\x08", // EBMLMaxSizeLength 8
		"\x42\x82", 0x80|len(docType), docType,
		"\x42\x87\x81\x04", // DocTypeVersion 4
		"\x42\x85\x81\x02", // DocTypeReadVersion 2
	)
	return join("\x1a\x45\xdf\xa3", 0x80|len(body), body, "\x18\x53\x80\x67\x01\xff\xff\xff\xff\xff\xff\xff")
}

func aiffFile(form string) []byte {
	comm := join("COMM", be32(18), be16(2), be32(0), be16(16), "\x40\x0e\xac\x44\x00\x00\x00\x00\x00\x00")
	return join("FORM", be32(uint32(4+len(comm))), form, comm)
}

// mpegFrames returns n silent MPEG-1 Layer III frames at 128 kbit/s and 44.1 kHz,
// 417 bytes each.
func mpegFrames(n int) []byte {
	var out []byte
	for range n {
		out = append(out, join("\xff\xfb\x90\x00", zeros(413))...)
	}
	return out
}

// adtsFrames returns n AAC-LC ADTS frames, stereo at 44.1 kHz, 32 bytes each.
func adtsFrames(n int) []byte {
	const size = 32
	var out []byte
	for range n {
		out = append(out, 0xff, 0xf1, 0x50, 0x80|size>>11, size>>3&0xff, size&7<<5|0x1f, 0xfc)
		out = append(out, zeros(size-7)...)
	}
	return out
}

// id3Tag returns an ID3v2.3 tag of size bytes after its header: a title frame,
// then padding.
func id3Tag(size int) []byte {
	frame := join("TIT2", be32(5), "\x00\x00", "\x00Song")
	return join("ID3\x03\x00\x00", size>>21&0x7f, size>>14&0x7f, size>>7&0x7f, size&0x7f, frame, zeros(size-len(frame)))
}

// flacFile returns a FLAC stream's marker and its STREAMINFO block: 44.1 kHz,
// stereo, 16 bits.
func flacFile() []byte {
	return join("fLaC", "\x80\x00\x00\x22", be16(4096), be16(4096), zeros(6), "\x0a\xc4\x42\xf0", zeros(4), zeros(16))
}

// oggPage returns the first page of an Ogg stream, holding one packet.
func oggPage(packet []byte) []byte {
	return join("OggS", 0, 2, zeros(8), le32(1), le32(0), le32(0), 1, len(packet), packet)
}

// swfFile returns a one-frame SWF: uncompressed for "FWS", zlib for "CWS".
func swfFile(tb testing.TB, sig string) []byte {
	tb.Helper()
	body := join("\x78\x00\x05\x5f\x00\x00\x0f\xa0\x00", "\x00\x18", le16(1), "\x40\x00", "\x00\x00")
	n := le32(uint32(8 + len(body)))
	if sig == "FWS" {
		return join(sig, 10, n, body)
	}
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	if _, err := w.Write(body); err != nil {
		tb.Fatal(err)
	}
	if err := w.Close(); err != nil {
		tb.Fatal(err)
	}
	return join(sig, 10, n, b.Bytes())
}

// sfntFile returns an OpenType font's header and table directory, of two tables.
func sfntFile(version string) []byte {
	return join(version, be16(2), be16(32), be16(1), be16(0),
		"cmap", be32(0), be32(44), be32(4),
		"head", be32(0), be32(48), be32(54),
		zeros(58))
}

func peFile() []byte {
	dos := join("MZ", le16(0x90), le16(3), le16(0), le16(4), le16(0), le16(0xffff), le16(0), le16(0xb8), zeros(0x3c-0x12), le32(0x80))
	return join(dos, zeros(0x80-len(dos)), "PE\x00\x00", le16(0x8664), le16(1), zeros(16))
}

func gzipFile(tb testing.TB) []byte {
	tb.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write([]byte("hello, sniff\n")); err != nil {
		tb.Fatal(err)
	}
	if err := w.Close(); err != nil {
		tb.Fatal(err)
	}
	return b.Bytes()
}

func tarFile(tb testing.TB, format tar.Format) []byte {
	tb.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	body := "hello, sniff\n"
	h := &tar.Header{Name: "hello.txt", Mode: 0o644, Size: int64(len(body)), ModTime: time.Unix(1700000000, 0), Format: format}
	if err := w.WriteHeader(h); err != nil {
		tb.Fatal(err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		tb.Fatal(err)
	}
	if err := w.Close(); err != nil {
		tb.Fatal(err)
	}
	return b.Bytes()
}

// utf16Text encodes s as UTF-16 behind a byte order mark.
func utf16Text(bo binary.AppendByteOrder, s string) []byte {
	out := bo.AppendUint16(nil, 0xfeff)
	for _, u := range utf16.Encode([]rune(s)) {
		out = bo.AppendUint16(out, u)
	}
	return out
}

// utf32Text encodes s as UTF-32 behind a byte order mark.
func utf32Text(bo binary.AppendByteOrder, s string) []byte {
	out := bo.AppendUint32(nil, 0xfeff)
	for _, r := range s {
		out = bo.AppendUint32(out, uint32(r))
	}
	return out
}

// zipEntry is a file in a test archive, deflated unless stored.
type zipEntry struct {
	name, body string
	store      bool
}

func zipFile(tb testing.TB, entries ...zipEntry) []byte {
	tb.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.store {
			h.Method = zip.Store
		}
		f, err := w.CreateHeader(h)
		if err != nil {
			tb.Fatal(err)
		}
		if _, err := f.Write([]byte(e.body)); err != nil {
			tb.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		tb.Fatal(err)
	}
	return b.Bytes()
}

// ooxml returns the entries of an Office Open XML package whose main part is
// part.
func ooxml(part string) []zipEntry {
	return []zipEntry{
		{name: "[Content_Types].xml", body: `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`},
		{name: "_rels/.rels", body: `<?xml version="1.0"?><Relationships/>`},
		{name: "docProps/core.xml", body: `<?xml version="1.0"?><cp:coreProperties/>`},
		{name: part, body: `<?xml version="1.0"?><document/>`},
	}
}

// odf returns the entries of an OpenDocument package of the given media type.
func odf(mediaType string) []zipEntry {
	return []zipEntry{
		{name: "mimetype", body: mediaType, store: true},
		{name: "content.xml", body: `<?xml version="1.0"?><office:document-content/>`},
		{name: "META-INF/manifest.xml", body: `<?xml version="1.0"?><manifest:manifest/>`},
	}
}

func epub() []zipEntry {
	return []zipEntry{
		{name: "mimetype", body: "application/epub+zip", store: true},
		{name: "META-INF/container.xml", body: `<?xml version="1.0"?><container/>`},
		{name: "OEBPS/content.opf", body: `<?xml version="1.0"?><package/>`},
	}
}

// oleEntry is a directory entry of a test compound file. Its IDs index the
// entries slice it is built from; none means no entry.
type oleEntry struct {
	name               string
	typ                byte // 1 storage, 2 stream, 5 root
	left, right, child uint32
}

const (
	none       = 0xffffffff
	endOfChain = 0xfffffffe
	fatSect    = 0xfffffffd
	freeSect   = 0xffffffff
)

func rootEntry(child uint32) oleEntry { return oleEntry{"Root Entry", 5, none, none, child} }
func stream(name string, left, right uint32) oleEntry {
	return oleEntry{name, 2, left, right, none}
}
func storage(name string, left, right, child uint32) oleEntry {
	return oleEntry{name, 1, left, right, child}
}

// chain returns a root whose children hang off one another's right sibling: a
// degenerate tree, which a reader must walk as surely as a balanced one.
func chain(names ...string) []oleEntry {
	es := []oleEntry{rootEntry(1)}
	for i, n := range names {
		right := uint32(i + 2)
		if i == len(names)-1 {
			right = none
		}
		es = append(es, stream(n, none, right))
	}
	return es
}

// oleFile builds a compound file with sectors of 1<<shift bytes: the header, one
// FAT sector (sector 0), and the directory from sector 1, chained through the FAT
// across as many sectors as the entries fill.
func oleFile(shift uint, entries []oleEntry) []byte {
	ss := 1 << shift
	per := ss / 128
	sectors := max(1, (len(entries)+per-1)/per)

	header := make([]byte, ss) // a 4 KiB sector holds the 512-byte header and zeros
	copy(header, oleHeader(shift, 1, endOfChain, 0))
	if shift == 12 {
		put32(header, 0x28, uint32(sectors))
	}

	fat := fill(ss, freeSect)
	put32(fat, 0, fatSect)
	for s := 1; s <= sectors; s++ {
		next := uint32(s + 1)
		if s == sectors {
			next = endOfChain
		}
		put32(fat, 4*s, next)
	}

	var dir []byte
	for _, en := range entries {
		dir = append(dir, en.bytes()...)
	}
	for len(dir) < sectors*ss {
		dir = append(dir, oleEntry{left: none, right: none, child: none}.bytes()...)
	}
	return join(header, fat, dir)
}

// oleHeader returns a 512-byte compound file header for sectors of 1<<shift
// bytes, with the directory starting at sector dir, DIFAT sectors from difat on,
// and the header's first FAT location fat0. The other 108 are free.
func oleHeader(shift uint, dir, difat, fat0 uint32) []byte {
	h := make([]byte, 512)
	copy(h, "\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1")
	put16(h, 0x18, 0x3e)
	put16(h, 0x1a, 3)
	if shift == 12 {
		put16(h, 0x1a, 4)
	}
	put16(h, 0x1c, 0xfffe)
	put16(h, 0x1e, uint16(shift))
	put16(h, 0x20, 6)
	put32(h, 0x2c, 1) // FAT sectors
	put32(h, 0x30, dir)
	put32(h, 0x38, 4096)
	put32(h, 0x3c, endOfChain) // no mini FAT
	put32(h, 0x44, difat)
	if difat != endOfChain {
		put32(h, 0x48, 1)
	}
	put32(h, 0x4c, fat0)
	for i := 1; i < 109; i++ {
		put32(h, 0x4c+4*i, freeSect)
	}
	return h
}

// bytes encodes a directory entry: 128 bytes, the name in UTF-16.
func (en oleEntry) bytes() []byte {
	e := make([]byte, 128)
	name := utf16.Encode([]rune(en.name))
	for j, u := range name {
		put16(e, 2*j, u)
	}
	if len(name) > 0 {
		put16(e, 0x40, uint16(2*(len(name)+1)))
	}
	e[0x42] = en.typ
	e[0x43] = 1 // black
	put32(e, 0x44, en.left)
	put32(e, 0x48, en.right)
	put32(e, 0x4c, en.child)
	put32(e, 0x74, endOfChain) // an empty stream
	return e
}

// fill returns n bytes of the 32-bit value v, repeated.
func fill(n int, v uint32) []byte {
	b := make([]byte, n)
	for i := 0; i+4 <= n; i += 4 {
		put32(b, i, v)
	}
	return b
}

func put16(b []byte, off int, v uint16) { binary.LittleEndian.PutUint16(b[off:], v) }
func put32(b []byte, off int, v uint32) { binary.LittleEndian.PutUint32(b[off:], v) }
