package sniff

import (
	"encoding/binary"
	"io"
	"math/bits"
	"strings"
)

// isoBMFF names an ISO base media file (MP4, QuickTime, HEIC, AVIF, 3GP, CR3) by
// the brands in its leading 'ftyp' box. The major brand says what the file is,
// unless it is generic ("isom", "mp42", "mif1"); then the compatible brands may
// say more.
func isoBMFF(b []byte) string {
	if len(b) < 12 || string(b[4:8]) != "ftyp" {
		return ""
	}
	size := binary.BigEndian.Uint32(b)
	if size < 12 || size > HeadSize {
		return "" // an ftyp box lists a few brands; a bigger one is not a box
	}
	major := brand(string(b[8:12]))
	if major != "" && major != "mp4" && major != "heif" {
		return major
	}
	best := major
	end := min(int(size), len(b))
	for i := 16; i+4 <= end; i += 4 {
		if t := brand(string(b[i : i+4])); brandRank[t] > brandRank[best] {
			best = t
		}
	}
	return best
}

// brand maps one ftyp brand to the type it marks, or "" for a brand it does not
// know.
func brand(s string) string {
	if t, ok := brands[s]; ok {
		return t
	}
	if strings.HasPrefix(s, "3g") {
		return "3gp" // 3GPP and 3GPP2 number their brands: 3gp4, 3gp6, 3g2a
	}
	return ""
}

var brands = map[string]string{
	"isom": "mp4", "iso2": "mp4", "iso3": "mp4", "iso4": "mp4", "iso5": "mp4",
	"iso6": "mp4", "iso7": "mp4", "iso8": "mp4", "iso9": "mp4",
	"mp41": "mp4", "mp42": "mp4", "mp71": "mp4", "avc1": "mp4", "dash": "mp4",
	"msnv": "mp4", "MSNV": "mp4", "mmp4": "mp4", "f4v ": "mp4", "f4p ": "mp4",
	"NDAS": "mp4", "XAVC": "mp4",
	"M4A ": "m4a", "M4B ": "m4a", "M4P ": "m4a",
	"M4V ": "m4v", "M4VH": "m4v", "M4VP": "m4v",
	"qt  ": "mov",
	"heic": "heic", "heix": "heic", "heim": "heic", "heis": "heic",
	"hevc": "heic", "hevx": "heic", "hevm": "heic", "hevs": "heic",
	"mif1": "heif", "mif2": "heif", "msf1": "heif", "miaf": "heif",
	"avif": "avif", "avis": "avif",
	"crx ": "cr3",
}

// brandRank orders the types a compatible brand can imply, from the least telling
// to the most. A generic MP4 that lists "M4A " is audio, unless it also lists
// "M4V ": a video's AAC soundtrack makes it compatible with both. One that lists
// "qt  " is still an MP4.
var brandRank = map[string]int{
	"mov": 1, "3gp": 2, "mp4": 3, "heif": 4, "m4a": 5, "m4v": 6, "cr3": 7, "heic": 8, "avif": 9,
}

// ebml names a Matroska or WebM file by the DocType in its EBML header, the first
// element of the file. The two share everything else.
func ebml(b []byte) string {
	if !hasPrefix(b, "\x1a\x45\xdf\xa3") {
		return ""
	}
	_, size, n := vint(b[4:])
	if n == 0 {
		return ""
	}
	body := b[4+n:]
	if size < uint64(len(body)) {
		body = body[:size]
	}
	for len(body) > 0 {
		id, _, n := vint(body)
		if n == 0 || n > 4 {
			return ""
		}
		_, size, m := vint(body[n:])
		if m == 0 {
			return ""
		}
		body = body[n+m:]
		if size > uint64(len(body)) {
			return ""
		}
		if id == 0x4282 { // DocType
			switch strings.TrimRight(string(body[:size]), "\x00") {
			case "webm":
				return "webm"
			case "matroska":
				return "mkv"
			}
			return ""
		}
		body = body[size:]
	}
	return ""
}

// vint reads an EBML variable-length integer, whose first byte's leading zero
// bits count the bytes after it. It returns the integer as stored (element IDs
// compare in that form), its value with the length marker cleared (sizes read in
// that form), and its length: 0 when b is too short or starts with a zero byte.
func vint(b []byte) (raw, val uint64, n int) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0, 0
	}
	n = bits.LeadingZeros8(b[0]) + 1
	if len(b) < n {
		return 0, 0, 0
	}
	for _, c := range b[:n] {
		raw = raw<<8 | uint64(c)
	}
	return raw, raw &^ (1 << (7 * n)), n
}

// id3 reads past an ID3v2 tag, which comes before the audio and says nothing of
// its format. What follows is almost always MP3; where the head shows it, FLAC
// and AAC are told apart.
func id3(b []byte) string {
	end, ok := id3End(b)
	if !ok {
		return ""
	}
	if end >= len(b) {
		return "mp3" // the tag runs past the head; DetectFile looks behind it
	}
	return afterID3(b[end:])
}

// id3End returns where an ID3v2 tag at the start of b ends. It validates the
// header first: version 2 to 4, no undefined flags, and size bytes that keep
// their top bit clear.
func id3End(b []byte) (int, bool) {
	if len(b) < 10 || string(b[:3]) != "ID3" || b[3] < 2 || b[3] > 4 || b[4] == 0xff || b[5]&0x0f != 0 {
		return 0, false
	}
	size := 0
	for _, c := range b[6:10] {
		if c&0x80 != 0 {
			return 0, false
		}
		size = size<<7 | int(c)
	}
	end := 10 + size
	if b[3] == 4 && b[5]&0x10 != 0 {
		end += 10 // a footer repeats the header
	}
	return end, true
}

// afterID3 names the audio that follows an ID3 tag.
func afterID3(b []byte) string {
	switch {
	case hasPrefix(b, "fLaC"):
		return "flac"
	case adtsFrame(b) > 0:
		return "aac"
	}
	return "mp3"
}

// id3Type looks behind an ID3 tag that runs past the head, which album art makes
// common, to tell FLAC and AAC from MP3 by the audio's first bytes.
func id3Type(r io.ReaderAt, size int64, head []byte) (string, error) {
	end, ok := id3End(head)
	if !ok || end < len(head) || int64(end) >= size {
		return "mp3", nil
	}
	b := make([]byte, min(size-int64(end), 16))
	if err := readAt(r, size, b, int64(end)); err != nil {
		return failed("mp3", err)
	}
	return afterID3(b), nil
}

// flac checks that the first metadata block after "fLaC" is STREAMINFO, as the
// format requires, at its fixed 34 bytes.
func flac(b []byte) string {
	if len(b) >= 8 && hasPrefix(b, "fLaC") && b[4]&0x7f == 0 && string(b[5:8]) == "\x00\x00\x22" {
		return "flac"
	}
	return ""
}

// ogg names an Ogg file by its first packet. Opus announces itself with
// "OpusHead"; Vorbis, FLAC, Speex and Theora are all plain Ogg here.
func ogg(b []byte) string {
	if len(b) < 27 || string(b[:5]) != "OggS\x00" {
		return ""
	}
	if hasPrefix(from(b, 27+int(b[26])), "OpusHead") { // past the segment table
		return "opus"
	}
	return "ogg"
}

// mpegBitrates are MPEG audio bitrates in kbit/s, by version (MPEG-1, then
// MPEG-2 and 2.5), layer (I, II, III) and the header's bitrate index.
var mpegBitrates = [2][3][16]int{
	{
		{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 0},
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 0},
		{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},
	},
	{
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 0},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
	},
}

// mpegRates are sample rates in Hz, by the header's version bits (MPEG-2.5,
// reserved, MPEG-2, MPEG-1) and its rate index.
var mpegRates = [4][3]int{{11025, 12000, 8000}, {}, {22050, 24000, 16000}, {44100, 48000, 32000}}

// mpegFrame decodes an MPEG audio frame header. It returns the frame's length in
// bytes, 0 when the four bytes are not a valid header, and the fields every frame
// of a stream shares: version, layer and sample rate.
func mpegFrame(b []byte) (n int, fixed uint16) {
	if len(b) < 4 || b[0] != 0xff || b[1]&0xe0 != 0xe0 {
		return 0, 0
	}
	ver, layer := b[1]>>3&3, b[1]>>1&3
	bitrate, rate, pad := b[2]>>4, b[2]>>2&3, int(b[2]>>1&1)
	if ver == 1 || layer == 0 || bitrate == 0 || bitrate == 15 || rate == 3 || b[3]&3 == 2 {
		return 0, 0 // reserved values, and free format, whose length cannot be known
	}
	v := 0
	if ver != 3 {
		v = 1
	}
	bps := mpegBitrates[v][3-layer][bitrate] * 1000
	hz := mpegRates[ver][rate]
	switch {
	case layer == 3: // Layer I counts in 4-byte slots
		n = (12*bps/hz + pad) * 4
	case layer == 1 && ver != 3: // Layer III of MPEG-2 and 2.5 has half the samples
		n = 72*bps/hz + pad
	default:
		n = 144*bps/hz + pad
	}
	return n, uint16(b[1]&0xfe)<<8 | uint16(b[2]&0x0c)
}

// mpegAudio reports whether b starts with two MPEG audio frames in a row that
// agree on version, layer and sample rate. One header alone is four bytes that
// other data can imitate; the frame after it seldom is.
func mpegAudio(b []byte) bool {
	n, fixed := mpegFrame(b)
	if n == 0 {
		return false
	}
	m, next := mpegFrame(from(b, n))
	return m > 0 && next == fixed
}

// adtsFrame decodes an AAC ADTS frame header: the sync word, layer 0, a defined
// sample rate. It returns the frame's length in bytes, or 0 when the seven bytes
// are not a valid header.
func adtsFrame(b []byte) int {
	if len(b) < 7 || b[0] != 0xff || b[1]&0xf6 != 0xf0 || b[2]>>2&0x0f > 12 {
		return 0
	}
	n := int(b[3]&3)<<11 | int(b[4])<<3 | int(b[5]>>5)
	if n < 7 {
		return 0
	}
	return n
}

// adts reports whether b starts with two ADTS frames in a row.
func adts(b []byte) bool {
	n := adtsFrame(b)
	return n > 0 && adtsFrame(from(b, n)) > 0
}
