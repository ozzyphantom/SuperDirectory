package pdf

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
)

// xrefEntry says where an object lives: at a file offset, or inside an object stream.
type xrefEntry struct {
	off  int64  // file offset, or for inStream the object stream's number
	idx  uint32 // index inside the object stream
	gen  uint16
	kind uint8
}

const (
	inFile   = 1
	inStream = 2
)

const xrefPage = 1024

// xrefTable maps object numbers to entries. It allocates in pages of 1024, so a
// file naming one high object number costs one page, not a million entries.
type xrefTable struct {
	pages [][]xrefEntry
}

func (t *xrefTable) get(num uint32) (xrefEntry, bool) {
	p := int(num / xrefPage)
	if p >= len(t.pages) || t.pages[p] == nil {
		return xrefEntry{}, false
	}
	e := t.pages[p][num%xrefPage]
	return e, e.kind != 0
}

// set records e unless num already has an entry: cross-reference sections are
// read newest first, and the newest wins. put overwrites, for a scan, where the
// newest comes last.
func (t *xrefTable) set(num uint32, e xrefEntry) { t.store(num, e, false) }
func (t *xrefTable) put(num uint32, e xrefEntry) { t.store(num, e, true) }

func (t *xrefTable) store(num uint32, e xrefEntry, over bool) {
	if num == 0 || num >= maxObjects {
		return
	}
	p := int(num / xrefPage)
	if p >= len(t.pages) {
		t.pages = append(t.pages, make([][]xrefEntry, p+1-len(t.pages))...)
	}
	if t.pages[p] == nil {
		t.pages[p] = make([]xrefEntry, xrefPage)
	}
	if over || t.pages[p][num%xrefPage].kind == 0 {
		t.pages[p][num%xrefPage] = e
	}
}

// loadXref reads the cross-reference sections from startxref back through /Prev.
// Newer sections win; their trailers are merged the same way.
func (d *Doc) loadXref() error {
	off, err := d.startxref()
	if err != nil {
		return err
	}
	d.trailer = dict{}
	seen := map[int64]bool{}
	for i := 0; i < maxSections && off >= 0 && !seen[off]; i++ {
		seen[off] = true
		trailer, prev, err := d.section(off)
		if err != nil {
			if i == 0 {
				return err
			}
			break // keep what the newer sections gave
		}
		for k, v := range trailer {
			if _, ok := d.trailer[k]; !ok {
				d.trailer[k] = v
			}
		}
		off = prev
	}
	return nil
}

// startxref finds the offset of the last cross-reference section.
func (d *Doc) startxref() (int64, error) {
	n := min(d.size, 4096)
	buf := make([]byte, n)
	m, _ := d.r.ReadAt(buf, d.size-n)
	buf = buf[:m]
	i := bytes.LastIndex(buf, []byte("startxref"))
	if i < 0 {
		return 0, errors.New("pdf: no startxref")
	}
	tok, _ := newLexer(buf[i+len("startxref"):]).token()
	off, ok := tok.(int64)
	if !ok || off < 0 || off >= d.size {
		return 0, errors.New("pdf: bad startxref offset")
	}
	return off, nil
}

// prevOf returns a section's /Prev offset, or -1.
func (d *Doc) prevOf(trailer dict) int64 {
	if p, ok := trailer["Prev"].(int64); ok && p >= 0 && p < d.size {
		return p
	}
	return -1
}

// section reads the cross-reference section at off: a classic table, or a
// cross-reference stream. It returns the trailer and the previous section's offset.
func (d *Doc) section(off int64) (dict, int64, error) {
	l := d.fileLexer(off)
	tok, err := l.token()
	if err != nil {
		return nil, -1, fmt.Errorf("pdf: reading xref at %d: %w", off, err)
	}
	if k, ok := tok.(keyword); ok && k == "xref" {
		return d.classicSection(l)
	}
	hdr, err := d.xrefStream(off, &d.xref, false)
	if err != nil {
		return nil, -1, err
	}
	return hdr, d.prevOf(hdr), nil
}

func (d *Doc) classicSection(l *lexer) (dict, int64, error) {
	for {
		tok, err := l.token()
		if err != nil {
			return nil, -1, fmt.Errorf("pdf: reading xref table: %w", err)
		}
		switch t := tok.(type) {
		case keyword:
			if t != "trailer" {
				return nil, -1, fmt.Errorf("pdf: unexpected %q in xref table", t)
			}
			v, err := l.object(0)
			trailer, ok := v.(dict)
			if err != nil || !ok {
				return nil, -1, errors.New("pdf: bad trailer dictionary")
			}
			// A hybrid file lists its compressed objects in a cross-reference stream
			// too. They rank after this table's entries and before /Prev's.
			if xs, ok := trailer["XRefStm"].(int64); ok && xs > 0 && xs < d.size {
				d.xrefStream(xs, &d.xref, false) // best effort: the table still stands
			}
			return trailer, d.prevOf(trailer), nil
		case int64:
			tok, _ := l.token()
			count, ok := tok.(int64)
			if !ok || t < 0 || count < 0 {
				return nil, -1, errors.New("pdf: bad xref subsection header")
			}
			d.subsection(l, t, count)
		default:
			return nil, -1, errors.New("pdf: bad xref table")
		}
	}
}

// subsection reads count entries of the form "offset gen n|f". A subsection that
// holds fewer entries than it claims ends at the first token that is not one.
func (d *Doc) subsection(l *lexer, start, count int64) {
	for i := int64(0); i < count; i++ {
		var toks [3]any
		n := 0
		for ; n < 3; n++ {
			t, err := l.token()
			if err != nil {
				break
			}
			toks[n] = t
		}
		off, ok1 := toks[0].(int64)
		gen, ok2 := toks[1].(int64)
		kind, ok3 := toks[2].(keyword)
		if n < 3 || !ok1 || !ok2 || !ok3 || (kind != "n" && kind != "f") {
			for j := n - 1; j >= 0; j-- {
				l.back = append(l.back, toks[j])
			}
			return
		}
		// Some writers number the first subsection from 1 although it starts with
		// object 0, the head of the free list.
		if i == 0 && start == 1 && off == 0 && gen == 65535 && kind == "f" {
			start = 0
		}
		num := start + i
		if kind == "n" && num > 0 && num < maxObjects && off > 0 && off < d.size && gen >= 0 && gen <= 0xFFFF {
			d.xref.set(uint32(num), xrefEntry{off: off, gen: uint16(gen), kind: inFile})
		}
	}
}

// xrefStream reads the cross-reference stream at off into t and returns its
// dictionary. With packedOnly, only entries for objects in object streams are
// kept: a scan trusts its own offsets over a damaged file's.
func (d *Doc) xrefStream(off int64, t *xrefTable, packedOnly bool) (dict, error) {
	v, _, err := d.parseAt(off, 0)
	if err != nil {
		return nil, err
	}
	s, ok := v.(*stream)
	if !ok {
		return nil, errors.New("pdf: xref is neither a table nor a stream")
	}
	if typ, _ := s.hdr["Type"].(name); typ != "XRef" {
		return nil, errors.New("pdf: xref stream lacks /Type /XRef")
	}
	data, err := d.decode(s)
	if err != nil {
		return nil, fmt.Errorf("pdf: decoding xref stream: %w", err)
	}
	w, _ := s.hdr["W"].(array)
	if len(w) < 3 {
		return nil, errors.New("pdf: xref stream has a bad /W")
	}
	var ws [3]int
	for i := range ws {
		n, ok := w[i].(int64)
		if !ok || n < 0 || n > 8 {
			return nil, errors.New("pdf: xref stream has a bad /W")
		}
		ws[i] = int(n)
	}
	row := ws[0] + ws[1] + ws[2]
	if row == 0 {
		return nil, errors.New("pdf: xref stream has empty rows")
	}
	index, _ := s.hdr["Index"].(array)
	if index == nil {
		size, _ := s.hdr["Size"].(int64)
		index = array{int64(0), size}
	}
	pos := 0
	for i := 0; i+1 < len(index); i += 2 {
		start, ok1 := index[i].(int64)
		count, ok2 := index[i+1].(int64)
		if !ok1 || !ok2 || start < 0 || count < 0 {
			break
		}
		for j := int64(0); j < count && pos+row <= len(data); j++ {
			f := data[pos : pos+row]
			pos += row
			typ := uint64(1) // a zero-width type field means type 1
			if ws[0] > 0 {
				typ = be(f[:ws[0]])
			}
			a, b := be(f[ws[0]:ws[0]+ws[1]]), be(f[ws[0]+ws[1]:])
			num := start + j
			if num <= 0 || num >= maxObjects {
				continue
			}
			switch typ {
			case 1:
				if !packedOnly && a > 0 && a < uint64(d.size) && b <= 0xFFFF {
					t.set(uint32(num), xrefEntry{off: int64(a), gen: uint16(b), kind: inFile})
				}
			case 2:
				if a > 0 && a < maxObjects && b < maxObjects && int64(a) != num {
					t.set(uint32(num), xrefEntry{off: int64(a), idx: uint32(b), kind: inStream})
				}
			}
		}
	}
	return s.hdr, nil
}

// be reads a big-endian unsigned integer of up to 8 bytes.
func be(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

// parseAt parses the indirect object "num gen obj ..." at off. want, when not 0,
// is the number the object must have. A stream's data is left in the file.
func (d *Doc) parseAt(off int64, want uint32) (any, uint16, error) {
	if off < 0 || off >= d.size {
		return nil, 0, fmt.Errorf("pdf: offset %d out of range", off)
	}
	l := d.fileLexer(off)
	t1, _ := l.token()
	t2, _ := l.token()
	t3, _ := l.token()
	num, ok1 := t1.(int64)
	gen, ok2 := t2.(int64)
	kw, ok3 := t3.(keyword)
	if !ok1 || !ok2 || !ok3 || kw != "obj" || num <= 0 || num >= maxObjects || gen < 0 || gen > 0xFFFF {
		return nil, 0, fmt.Errorf("pdf: no object header at offset %d", off)
	}
	if want != 0 && uint32(num) != want {
		return nil, 0, fmt.Errorf("pdf: found object %d where %d should be", num, want)
	}
	g := uint16(gen)
	v, err := l.object(0)
	if err != nil {
		return nil, 0, fmt.Errorf("pdf: object %d: %w", num, err)
	}
	if k, ok := v.(keyword); ok && ends(k) {
		return nil, g, nil // "n g obj endobj": an empty object reads as null
	}
	hdr, ok := v.(dict)
	if !ok {
		return v, g, nil
	}
	if tok, err := l.token(); err == nil {
		if k, ok := tok.(keyword); ok && k == "stream" {
			// The data starts after the end of line that follows the keyword.
			if b, ok := l.next(); ok {
				switch b {
				case '\r':
					if c, ok := l.next(); ok && c != '\n' {
						l.unread()
					}
				case '\n':
				default:
					l.unread()
				}
			}
			return &stream{hdr: hdr, off: l.offset(), ref: ref{uint32(num), g}}, g, nil
		}
	}
	return hdr, g, nil
}

// object returns the value of the indirect object r, or nil when it cannot be read.
func (d *Doc) object(r ref) any {
	if r.num == 0 {
		return nil
	}
	if v, ok := d.objs[r.num]; ok {
		return v
	}
	// A reference back to an object being read is a cycle, and a deep chain of
	// reads (a /Length inside an object stream inside another...) is hostile.
	if d.busy[r.num] || d.depth >= 32 {
		return nil
	}
	d.busy[r.num] = true
	d.depth++
	v, err := d.load(r.num)
	d.depth--
	delete(d.busy, r.num)
	if err != nil {
		v = nil
	}
	if len(d.objs) < maxCached {
		d.objs[r.num] = v
	}
	return v
}

// maxCached caps the objects a Doc keeps parsed. Past it, an object is parsed
// again each time it is needed: slower, but memory stays bounded.
const maxCached = 200_000

// load reads object num through the cross-reference table, falling back to a
// scan of the file when the table is missing or wrong about it.
func (d *Doc) load(num uint32) (any, error) {
	if e, ok := d.xref.get(num); ok {
		if v, err := d.loadEntry(num, e); err == nil {
			return v, nil
		}
	}
	if e, ok := d.scanLookup(num); ok {
		return d.loadEntry(num, e)
	}
	return nil, fmt.Errorf("pdf: object %d not found", num)
}

func (d *Doc) loadEntry(num uint32, e xrefEntry) (any, error) {
	switch e.kind {
	case inFile:
		v, gen, err := d.parseAt(e.off, num)
		if err != nil {
			return nil, err
		}
		if d.sec != nil && num != d.sec.encNum {
			v = d.sec.decryptStrings(v, ref{num, gen})
		}
		return v, nil
	case inStream:
		return d.packed(uint32(e.off), e.idx, num)
	}
	return nil, fmt.Errorf("pdf: object %d not found", num)
}

// resolve follows references to a direct value.
func (d *Doc) resolve(v any) any {
	for range 16 {
		r, ok := v.(ref)
		if !ok {
			return v
		}
		v = d.object(r)
	}
	return nil
}

// dictOf resolves v to a dictionary; a stream yields its dictionary.
func (d *Doc) dictOf(v any) dict {
	switch t := d.resolve(v).(type) {
	case dict:
		return t
	case *stream:
		return t.hdr
	}
	return nil
}

func (d *Doc) intOf(v any) (int64, bool) {
	switch t := d.resolve(v).(type) {
	case int64:
		return t, true
	case float64:
		if t >= -1e15 && t <= 1e15 {
			return int64(t), true
		}
	}
	return 0, false
}

// objStm is a decoded object stream: its data and where each object starts.
type objStm struct {
	data  []byte
	nums  []uint32
	offs  []int          // offsets into data
	ends  []int          // where each object's bytes end: the next object's start
	index map[uint32]int // object number to position, built when an index is wrong
	bytes int
}

// stmCache keeps recently decoded object streams, up to 32 MiB of them.
type stmCache struct {
	m     map[uint32]*objStm
	order []uint32
	bytes int
}

const stmCacheBytes = 32 << 20

func (c *stmCache) get(num uint32) *objStm { return c.m[num] }

func (c *stmCache) add(num uint32, s *objStm) {
	if s.bytes > stmCacheBytes {
		return
	}
	if c.m == nil {
		c.m = map[uint32]*objStm{}
	}
	for len(c.order) > 0 && c.bytes+s.bytes > stmCacheBytes {
		old := c.order[0]
		c.order = c.order[1:]
		c.bytes -= c.m[old].bytes
		delete(c.m, old)
	}
	c.m[num] = s
	c.order = append(c.order, num)
	c.bytes += s.bytes
}

// objectStream decodes object stream num and reads its index.
func (d *Doc) objectStream(num uint32) (*objStm, error) {
	if s := d.stms.get(num); s != nil {
		return s, nil
	}
	st, ok := d.object(ref{num: num}).(*stream)
	if !ok {
		return nil, fmt.Errorf("pdf: object stream %d is not a stream", num)
	}
	n, ok1 := d.intOf(st.hdr["N"])
	first, ok2 := d.intOf(st.hdr["First"])
	if !ok1 || !ok2 || n < 0 || first < 0 {
		return nil, fmt.Errorf("pdf: object stream %d lacks /N or /First", num)
	}
	data, err := d.decode(st)
	if err != nil {
		return nil, fmt.Errorf("pdf: object stream %d: %w", num, err)
	}
	if first > int64(len(data)) {
		return nil, fmt.Errorf("pdf: object stream %d: /First beyond the data", num)
	}
	s := &objStm{data: data, bytes: len(data)}
	l := newLexer(data[:first])
	for i := int64(0); i < n && i < maxObjects; i++ {
		t1, err1 := l.token()
		t2, err2 := l.token()
		on, ok1 := t1.(int64)
		oo, ok2 := t2.(int64)
		if err1 != nil || err2 != nil || !ok1 || !ok2 || on <= 0 || on >= maxObjects || oo < 0 || first+oo > int64(len(data)) {
			break
		}
		s.nums = append(s.nums, uint32(on))
		s.offs = append(s.offs, int(first+oo))
	}
	// Each object ends where the next one in the data starts, whatever order the
	// header lists them in, so no object is read through the ones after it.
	sorted := append([]int(nil), s.offs...)
	sort.Ints(sorted)
	s.ends = make([]int, len(s.offs))
	for i, o := range s.offs {
		j := sort.SearchInts(sorted, o+1)
		s.ends[i] = len(data)
		if j < len(sorted) {
			s.ends[i] = sorted[j]
		}
	}
	d.stms.add(num, s)
	return s, nil
}

// packed reads object num, the idx-th object of object stream stm.
func (d *Doc) packed(stm, idx, num uint32) (any, error) {
	if stm == num {
		return nil, fmt.Errorf("pdf: object %d claims to be inside itself", num)
	}
	s, err := d.objectStream(stm)
	if err != nil {
		return nil, err
	}
	i := int(idx)
	if i >= len(s.nums) || s.nums[i] != num {
		// The index is wrong; the stream's own header still names the object.
		// A map, not a search, so a stream of a million wrong indexes stays linear.
		if s.index == nil {
			s.index = make(map[uint32]int, len(s.nums))
			for k, n := range s.nums {
				if _, dup := s.index[n]; !dup {
					s.index[n] = k
				}
			}
		}
		var ok bool
		if i, ok = s.index[num]; !ok {
			return nil, fmt.Errorf("pdf: object %d not in object stream %d", num, stm)
		}
	}
	// Read only up to the next object, so a bare number cannot take the next
	// object's tokens for "gen R".
	l := newLexer(s.data[s.offs[i]:s.ends[i]])
	l.refs = true
	v, err := l.object(0)
	if err != nil {
		return nil, fmt.Errorf("pdf: object %d in object stream %d: %w", num, stm, err)
	}
	if k, ok := v.(keyword); ok && ends(k) {
		return nil, nil
	}
	return v, nil
}

// scanned is the file indexed by scanning it for "num gen obj", for files whose
// cross-reference data is missing or wrong.
type scanned struct {
	objs     xrefTable
	trailer  dict
	stms     []uint32 // objects that look like object streams
	catalogs []uint32 // objects that look like the catalog
	pages    []uint32 // objects that look like pages, in file order
	packed   []uint32 // objects found inside object streams
	indexed  bool     // object stream members are in objs
}

// maxScan caps how much of a damaged file a rebuild reads.
const maxScan = 1 << 30

// scanLookup finds object num by scanning, indexing object streams only when the
// object is not a plain one.
func (d *Doc) scanLookup(num uint32) (xrefEntry, bool) {
	s := d.scanTable()
	if e, ok := s.objs.get(num); ok {
		return e, true
	}
	if !s.indexed {
		d.indexObjStms(s)
		return s.objs.get(num)
	}
	return xrefEntry{}, false
}

// scanTable scans the file once. The table is in place before the scan fills
// it, so a lookup made while scanning (a /Length, say) sees a partial table
// instead of starting another scan.
func (d *Doc) scanTable() *scanned {
	if d.scan == nil {
		d.scan = &scanned{trailer: dict{}}
		d.scanFile(d.scan)
	}
	return d.scan
}

// indexObjStms adds the members of every object stream the scan found.
func (d *Doc) indexObjStms(s *scanned) {
	s.indexed = true
	for _, n := range s.stms {
		os, err := d.objectStream(n)
		if err != nil {
			continue
		}
		for i, m := range os.nums {
			if _, ok := s.objs.get(m); !ok {
				s.objs.set(m, xrefEntry{off: int64(n), idx: uint32(i), kind: inStream})
				s.packed = append(s.packed, m)
			}
		}
	}
}

// scanFile reads the file looking for object headers, trailers, and the objects
// that matter when rebuilding: cross-reference streams, object streams, catalogs.
func (d *Doc) scanFile(s *scanned) {
	const chunk = 1 << 20
	const lead = 64 // bytes kept from the previous chunk, for headers on the boundary
	buf := make([]byte, min(d.size, chunk+lead+16))
	var (
		trailers []int64
		xrefs    []uint32
		cur      uint32 // the object being scanned through
	)
	mark := func(list *[]uint32) { // once per object: its markers come together
		if cur != 0 && (len(*list) == 0 || (*list)[len(*list)-1] != cur) {
			*list = append(*list, cur)
		}
	}
	limit := min(d.size, maxScan)
	for base := int64(0); base < limit; base += chunk {
		start := max(base-lead, 0)
		n, _ := d.r.ReadAt(buf[:min(int64(len(buf)), limit-start)], start)
		b := buf[:n]
		for i := int(base - start); i < n && int64(i)+start < base+chunk; i++ {
			switch b[i] {
			case 'o':
				if hasWord(b, i, "obj") {
					if num, at, ok := objHeader(b, i); ok {
						s.objs.put(num, xrefEntry{off: start + int64(at), kind: inFile})
						cur = num
					}
				}
			case 't':
				if hasWord(b, i, "trailer") {
					trailers = append(trailers, start+int64(i))
				}
			case '/':
				switch {
				case hasWord(b, i, "/XRef"):
					mark(&xrefs)
				case hasWord(b, i, "/ObjStm"):
					mark(&s.stms)
				case hasWord(b, i, "/Catalog"):
					mark(&s.catalogs)
				case hasWord(b, i, "/Page"):
					mark(&s.pages)
				}
			}
		}
	}

	// Merge the trailers and cross-reference stream dictionaries, later ones
	// winning, and keep the compressed entries of the streams, newest first.
	// Only the last few of each count: the newest win, and a hostile file can
	// hold millions of the words.
	const keep = 64
	trailers = trailers[max(0, len(trailers)-keep):]
	xrefs = xrefs[max(0, len(xrefs)-keep):]
	type found struct {
		off int64
		hdr dict
	}
	var all []found
	for _, off := range trailers {
		l := d.fileLexer(off)
		l.token() // the keyword
		if v, err := l.object(0); err == nil {
			if t, ok := v.(dict); ok {
				all = append(all, found{off, t})
			}
		}
	}
	for i := len(xrefs) - 1; i >= 0; i-- {
		e, _ := s.objs.get(xrefs[i])
		if hdr, err := d.xrefStream(e.off, &s.objs, true); err == nil {
			all = append(all, found{e.off, hdr})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].off < all[j].off })
	for _, f := range all {
		for _, k := range []name{"Root", "Info", "Encrypt", "ID"} {
			if v, ok := f.hdr[k]; ok {
				s.trailer[k] = v
			}
		}
	}
}

// hasWord reports whether b holds the keyword w at i, not glued to other
// regular characters.
func hasWord(b []byte, i int, w string) bool {
	if !bytes.HasPrefix(b[i:], []byte(w)) {
		return false
	}
	if j := i + len(w); j < len(b) && isRegular(b[j]) {
		return false
	}
	if w[0] != '/' && i > 0 && isRegular(b[i-1]) {
		return false
	}
	return true
}

// objHeader checks that "num gen " precedes the obj keyword at i, and returns the
// object number and the offset where the header starts.
func objHeader(b []byte, i int) (uint32, int, bool) {
	j := i - 1
	digits := func(maxLen int) (int, int, bool) {
		if j < 0 || !isSpace(b[j]) {
			return 0, 0, false
		}
		for j >= 0 && isSpace(b[j]) {
			j--
		}
		end := j + 1
		for j >= 0 && b[j] >= '0' && b[j] <= '9' && end-j <= maxLen {
			j--
		}
		start := j + 1
		return start, end, start < end
	}
	if _, _, ok := digits(5); !ok {
		return 0, 0, false
	}
	start, end, ok := digits(7)
	if !ok || (j >= 0 && isRegular(b[j])) {
		return 0, 0, false
	}
	var num uint32
	for _, c := range b[start:end] {
		num = num*10 + uint32(c-'0')
	}
	if num == 0 || num >= maxObjects {
		return 0, 0, false
	}
	return num, start, true
}

// useScan takes /Root and anything else the trailer lacks from a scan of the file.
func (d *Doc) useScan() {
	s := d.scanTable()
	if d.trailer == nil {
		d.trailer = dict{}
	}
	for k, v := range s.trailer {
		if _, ok := d.trailer[k]; !ok || k == "Root" {
			d.trailer[k] = v
		}
	}
}

// findCatalog looks for the document catalog among the objects the scan found,
// newest first, then among the objects inside object streams.
func (d *Doc) findCatalog() bool {
	s := d.scanTable()
	for i := len(s.catalogs) - 1; i >= 0; i-- {
		if r := (ref{num: s.catalogs[i]}); isCatalog(d.dictOf(r)) {
			d.trailer["Root"] = r
			return true
		}
	}
	if !s.indexed {
		d.indexObjStms(s)
	}
	for i := len(s.packed) - 1; i >= 0; i-- {
		if r := (ref{num: s.packed[i]}); isCatalog(d.dictOf(r)) {
			d.trailer["Root"] = r
			return true
		}
	}
	return false
}

func isCatalog(c dict) bool {
	if c == nil {
		return false
	}
	if t, _ := c["Type"].(name); t == "Catalog" {
		return true
	}
	_, ok := c["Pages"]
	return ok
}
