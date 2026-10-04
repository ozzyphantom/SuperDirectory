package pdf

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"slices"
)

var (
	errImage    = errors.New("pdf: image data is not decoded")
	errTooLarge = errors.New("pdf: stream too large")
	errBudget   = errors.New("pdf: document decodes to too much data")
)

// decode returns a stream's data with its filters undone, charged to the Doc's
// budget. Image data is refused: decoding it costs time and memory and holds no
// text.
func (d *Doc) decode(s *stream) ([]byte, error) {
	filters, parms, err := d.filters(s.hdr)
	if err != nil {
		return nil, err
	}
	for _, f := range filters {
		switch f {
		case "DCTDecode", "DCT", "JPXDecode", "CCITTFaxDecode", "CCF", "JBIG2Decode":
			return nil, errImage
		}
	}
	if _, ok := s.hdr["F"]; ok {
		return nil, errors.New("pdf: stream data lives in another file")
	}
	data, err := d.raw(s)
	if err != nil {
		return nil, err
	}
	if d.sec != nil {
		data = d.sec.decrypt(d.sec.streamMethod(s, filters, parms), s.ref, data)
	}
	if len(filters) == 0 {
		if err := d.charge(len(data)); err != nil {
			return nil, err
		}
		return data, nil
	}
	for i, f := range filters {
		limit := min(int64(maxStream), d.budget)
		switch f {
		case "FlateDecode", "Fl":
			if data, err = inflate(data, limit); err == nil {
				data, err = d.unpredict(data, parms[i])
			}
		case "LZWDecode", "LZW":
			early := int64(1)
			if v, ok := d.intOf(parms[i]["EarlyChange"]); ok && v == 0 {
				early = 0
			}
			if data, err = unlzw(data, int(early), limit); err == nil {
				data, err = d.unpredict(data, parms[i])
			}
		case "ASCIIHexDecode", "AHx":
			data = unhexData(data)
		case "ASCII85Decode", "A85":
			data = un85(data)
		case "RunLengthDecode", "RL":
			data, err = unrunlength(data, limit)
		case "Crypt":
			continue // undone with the rest of the decryption
		default:
			return nil, fmt.Errorf("pdf: unsupported filter %s", f)
		}
		if err != nil {
			return nil, fmt.Errorf("pdf: %s: %w", f, err)
		}
		if err := d.charge(len(data)); err != nil {
			return nil, err
		}
	}
	return data, nil
}

// charge takes n decoded bytes from the Doc's budget.
func (d *Doc) charge(n int) error {
	d.budget -= int64(n)
	if d.budget < 0 {
		d.budget = 0
		return errBudget
	}
	return nil
}

// filters lists a stream's filters and their parameters, one dictionary (perhaps
// nil) per filter.
func (d *Doc) filters(h dict) ([]name, []dict, error) {
	var fs []name
	switch f := d.resolve(h["Filter"]).(type) {
	case nil:
	case name:
		fs = []name{f}
	case array:
		if len(f) > 16 {
			return nil, nil, errors.New("pdf: too many filters")
		}
		for _, x := range f {
			n, ok := d.resolve(x).(name)
			if !ok {
				return nil, nil, errors.New("pdf: bad filter name")
			}
			fs = append(fs, n)
		}
	default:
		return nil, nil, errors.New("pdf: bad /Filter")
	}
	ps := make([]dict, len(fs))
	switch p := d.resolve(h["DecodeParms"]).(type) {
	case dict:
		if len(ps) > 0 {
			ps[0] = p
		}
	case array:
		for i := range min(len(p), len(ps)) {
			ps[i] = d.dictOf(p[i])
		}
	}
	return fs, ps, nil
}

// raw reads a stream's bytes as stored. /Length is trusted only when endstream
// follows where it says; otherwise the data runs to the endstream keyword.
func (d *Doc) raw(s *stream) ([]byte, error) {
	if s.off < 0 || s.off > d.size {
		return nil, errors.New("pdf: stream data out of range")
	}
	if n, ok := d.intOf(s.hdr["Length"]); ok && n >= 0 && n <= d.size-s.off && d.endstreamAt(s.off+n) {
		return d.readAt(s.off, n)
	}
	end, ok := d.findEndstream(s.off)
	if !ok {
		return nil, errors.New("pdf: stream has no endstream")
	}
	return d.readAt(s.off, end-s.off)
}

func (d *Doc) readAt(off, n int64) ([]byte, error) {
	if n > maxStream {
		return nil, errTooLarge
	}
	if n > d.budget {
		return nil, errBudget
	}
	b := make([]byte, n)
	m, err := d.r.ReadAt(b, off)
	if m < len(b) && err != nil && err != io.EOF {
		return nil, fmt.Errorf("pdf: reading stream: %w", err)
	}
	return b[:m], nil
}

// endstreamAt reports whether the endstream keyword (or, in a sloppy file, endobj)
// follows at pos, after optional white space.
func (d *Doc) endstreamAt(pos int64) bool {
	var buf [40]byte
	n, _ := d.r.ReadAt(buf[:], pos)
	b := bytes.TrimLeft(buf[:n], "\x00\t\n\f\r ")
	return bytes.HasPrefix(b, []byte("endstream")) || bytes.HasPrefix(b, []byte("endobj"))
}

// findEndstream finds the first endstream keyword after off, within the stream
// size cap, and returns where the data before it ends. What it reads counts
// against the Doc's allowance, so a thousand streams with a wrong /Length and no
// endstream cannot each read to the end of a large file.
func (d *Doc) findEndstream(off int64) (int64, bool) {
	const chunk = 64 << 10
	kw := []byte("endstream")
	buf := make([]byte, chunk+len(kw))
	for at := off; at < d.size && at-off <= maxStream && d.lexLeft > 0; at += chunk {
		n, _ := d.r.ReadAt(buf, at)
		if n == 0 {
			return 0, false
		}
		d.lexLeft -= int64(n)
		if i := bytes.Index(buf[:n], kw); i >= 0 {
			end := at + int64(i)
			// Drop the end of line that separates the data from the keyword.
			var eol [2]byte
			if end-2 >= off {
				d.r.ReadAt(eol[:], end-2)
				switch {
				case eol[0] == '\r' && eol[1] == '\n':
					end -= 2
				case eol[1] == '\n' || eol[1] == '\r':
					end--
				}
			} else if end-1 >= off {
				d.r.ReadAt(eol[1:], end-1)
				if eol[1] == '\n' || eol[1] == '\r' {
					end--
				}
			}
			return end, true
		}
	}
	return 0, false
}

// inflate undoes FlateDecode. A stream without a zlib header is tried as bare
// deflate data. A damaged tail keeps what decoded before it: real files are
// often truncated or carry a bad checksum.
func inflate(in []byte, limit int64) ([]byte, error) {
	var r io.Reader
	if zr, err := zlib.NewReader(bytes.NewReader(in)); err == nil {
		r = zr
	} else {
		r = flate.NewReader(bytes.NewReader(in))
	}
	out, err := readLimited(r, limit)
	if err == errTooLarge {
		return nil, err
	}
	if len(out) == 0 && err != nil && len(in) > 2 {
		// A bad zlib header in front of good deflate data.
		out, err = readLimited(flate.NewReader(bytes.NewReader(in[2:])), limit)
		if err == errTooLarge {
			return nil, err
		}
	}
	if len(out) == 0 && err != nil {
		return nil, err
	}
	return out, nil
}

// readLimited reads r to its end, failing when it yields more than limit bytes.
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	var out []byte
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if int64(len(out)+n) > limit {
			return nil, errTooLarge
		}
		out = append(out, buf[:n]...)
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

// unpredict undoes a TIFF or PNG predictor, the row filters that make tables such
// as cross-reference streams compress well.
func (d *Doc) unpredict(data []byte, p dict) ([]byte, error) {
	param := func(k name, def int64) int64 {
		if v, ok := d.intOf(p[k]); ok {
			return v
		}
		return def
	}
	pred := param("Predictor", 1)
	if pred <= 1 {
		return data, nil
	}
	colors, bpc, cols := param("Colors", 1), param("BitsPerComponent", 8), param("Columns", 1)
	if colors < 1 || colors > 64 || cols < 1 || cols > 1<<24 {
		return nil, errors.New("bad predictor parameters")
	}
	switch bpc {
	case 1, 2, 4, 8, 16:
	default:
		return nil, errors.New("bad predictor bits per component")
	}
	bpp := int((colors*bpc + 7) / 8)
	rowLen := (colors*bpc*cols + 7) / 8
	if rowLen > int64(len(data)) {
		rowLen = int64(len(data)) // the data holds one partial row at most
	}
	n := int(rowLen)
	if n == 0 {
		return data, nil
	}

	if pred == 2 { // TIFF: each byte is the difference from the one a pixel before
		if bpc != 8 {
			return nil, errors.New("TIFF predictor needs 8 bits per component")
		}
		out := append([]byte(nil), data...)
		for row := 0; row < len(out); row += n {
			r := out[row:min(row+n, len(out))]
			for i := bpp; i < len(r); i++ {
				r[i] += r[i-bpp]
			}
		}
		return out, nil
	}

	// PNG: each row starts with a byte naming its filter.
	out := make([]byte, 0, len(data)/(n+1)*n+n)
	prev := make([]byte, n)
	for pos := 0; pos < len(data); pos += n + 1 {
		ft := data[pos]
		src := data[pos+1 : min(pos+1+n, len(data))]
		start := len(out)
		out = append(out, src...)
		cur := out[start:]
		for i := range cur {
			var left, upLeft byte
			if i >= bpp {
				left, upLeft = cur[i-bpp], prev[i-bpp]
			}
			up := prev[i]
			switch ft {
			case 1:
				cur[i] += left
			case 2:
				cur[i] += up
			case 3:
				cur[i] += byte((int(left) + int(up)) / 2)
			case 4:
				cur[i] += paeth(left, up, upLeft)
			}
		}
		copy(prev, cur)
	}
	return out, nil
}

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := abs(p-int(a)), abs(p-int(b)), abs(p-int(c))
	switch {
	case pa <= pb && pa <= pc:
		return a
	case pb <= pc:
		return b
	}
	return c
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// unlzw undoes LZWDecode: codes of 9 to 12 bits, most significant bit first, with
// 256 as the clear code and 257 as the end. early is 1 when the code width grows
// one code early, the PDF default.
func unlzw(in []byte, early int, limit int64) ([]byte, error) {
	const clearCode, eod = 256, 257
	var (
		prefix [4096]int16
		suffix [4096]byte
		first  [4096]byte
		length [4096]int16
	)
	for i := range 256 {
		prefix[i], suffix[i], first[i], length[i] = -1, byte(i), byte(i), 1
	}
	var out []byte
	next, width, prev := 258, 9, -1
	var bits uint32
	var nbits, pos int
	for {
		for nbits < width && pos < len(in) {
			bits = bits<<8 | uint32(in[pos])
			pos++
			nbits += 8
		}
		if nbits < width {
			return out, nil // out of data without an end code: keep what came
		}
		code := int(bits>>(nbits-width)) & (1<<width - 1)
		nbits -= width
		bits &= 1<<nbits - 1
		switch {
		case code == clearCode:
			next, width, prev = 258, 9, -1
			continue
		case code == eod:
			return out, nil
		case prev < 0:
			if code > 255 {
				return out, errors.New("bad first code")
			}
			out = append(out, byte(code))
			prev = code
			continue
		case code > next || code == next && next >= 4096:
			return out, errors.New("bad code")
		}
		fb := first[code]
		if code == next {
			fb = first[prev]
		}
		if next < 4096 {
			prefix[next], suffix[next], first[next], length[next] = int16(prev), fb, first[prev], length[prev]+1
			next++
		}
		n := int(length[code])
		if int64(len(out)+n) > limit {
			return nil, errTooLarge
		}
		out = slices.Grow(out, n)[:len(out)+n]
		for c, i := code, len(out)-1; c >= 0 && i >= len(out)-n; i-- {
			out[i] = suffix[c]
			c = int(prefix[c])
		}
		prev = code
		if next+early >= 1<<width && width < 12 {
			width++
		}
	}
}

// unhexData undoes ASCIIHexDecode. White space and stray bytes are skipped; >
// ends the data, and an odd final digit is padded with zero.
func unhexData(in []byte) []byte {
	out := make([]byte, 0, len(in)/2)
	hi := -1
	for _, b := range in {
		if b == '>' {
			break
		}
		v := unhex(b)
		if v < 0 {
			continue
		}
		if hi < 0 {
			hi = v
		} else {
			out = append(out, byte(hi<<4|v))
			hi = -1
		}
	}
	if hi >= 0 {
		out = append(out, byte(hi<<4))
	}
	return out
}

// un85 undoes ASCII85Decode, up to ~>. Bytes outside the alphabet are skipped.
func un85(in []byte) []byte {
	in = bytes.TrimLeft(in, "\x00\t\n\f\r ")
	in = bytes.TrimPrefix(in, []byte("<~"))
	out := make([]byte, 0, len(in)*4/5+4)
	var group [5]byte
	n := 0
	flush := func(k int) { // the group's first k-1 bytes
		v := uint64(0)
		for _, g := range group {
			v = v*85 + uint64(g)
		}
		w := []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
		out = append(out, w[:k-1]...)
	}
loop:
	for _, b := range in {
		switch {
		case b == '~':
			break loop
		case b == 'z' && n == 0:
			out = append(out, 0, 0, 0, 0)
		case b >= '!' && b <= 'u':
			group[n] = b - '!'
			if n++; n == 5 {
				flush(5)
				n = 0
			}
		}
	}
	if n > 1 {
		for i := n; i < 5; i++ {
			group[i] = 84 // pad with u
		}
		flush(n)
	}
	return out
}

// unrunlength undoes RunLengthDecode.
func unrunlength(in []byte, limit int64) ([]byte, error) {
	var out []byte
	for i := 0; i < len(in); {
		n := int(in[i])
		i++
		switch {
		case n < 128: // n+1 literal bytes
			end := min(i+n+1, len(in))
			out = append(out, in[i:end]...)
			i = end
		case n > 128: // the next byte, 257-n times
			if i >= len(in) {
				return out, nil
			}
			for range 257 - n {
				out = append(out, in[i])
			}
			i++
		default:
			return out, nil
		}
		if int64(len(out)) > limit {
			return nil, errTooLarge
		}
	}
	return out, nil
}
