// Package pdf reads just enough of a PDF to get its title and its text.
//
// The app renames "doc_0042.pdf" to the title inside it and compares documents
// by their words, so the reader cares about words, not looks: nothing is
// rendered, image data is never decoded, and layout counts only as far as it
// separates one word or line from the next.
//
// Every file is untrusted. Offsets are bounds-checked, every allocation the file
// can steer is capped, and every loop is bounded by the data or by a limit, so a
// damaged or hostile file yields an error or empty results, never a crash.
package pdf

import (
	"bytes"
	"errors"
	"io"
)

// ErrEncrypted reports that the text or title is unavailable: the document is
// encrypted with a password, or by a scheme this package does not decrypt.
var ErrEncrypted = errors.New("pdf: document is encrypted")

// Limits on the work a file can ask for.
const (
	maxObjects   = 1_000_000  // cross-reference entries, and the highest object number
	maxStream    = 64 << 20   // one decoded stream
	maxDecoded   = 256 << 20  // all the decoding one Doc does
	maxLexed     = 32 << 20   // bytes one Doc reads to parse objects or find keywords, beyond four times the file's size
	maxDepth     = 64         // nesting of arrays and dictionaries
	maxFormDepth = 16         // form XObjects drawn inside form XObjects
	maxCMap      = 100_000    // entries in one character map
	maxTables    = 1_000_000  // character map and width entries across all of a Doc's fonts
	maxSections  = 1024       // cross-reference sections chained through /Prev
	maxPages     = 100_000    // pages walked in the page tree
	maxTextPages = 10_000     // pages whose text Text reads
	maxOps       = 50_000_000 // content stream operators one Doc runs
	defaultLimit = 2_000_000  // runes Text returns when given no limit
)

// Doc is an open PDF. It reads the file lazily, as Title, Text and Pages need
// it, and decodes at most 256 MiB of streams across all its calls. A Doc is not
// safe for concurrent use.
type Doc struct {
	r    io.ReaderAt
	size int64

	xref    xrefTable
	scan    *scanned // the file indexed by scanning; nil until a lookup needs it
	trailer dict

	objs  map[uint32]any  // objects read so far; nil for those that failed
	busy  map[uint32]bool // objects being read: a reference back to one is a cycle
	depth int             // object reads in progress, one inside another
	stms  stmCache

	sec       *security
	locked    bool // encrypted, and not with the empty password
	plainMeta bool // locked, but the XMP metadata is stored unencrypted

	budget  int64 // bytes this Doc may still decode
	lexLeft int64 // bytes it may still read from the file to parse or search
	ops     int64 // content stream operators it may still run

	fonts     map[uint32]*font
	cmaps     map[uint32]*cmap
	inlineFn  map[uintptr]inlineFont // fonts given as direct dictionaries
	tableLeft int                    // CMap and width entries the Doc may still hold

	pages     []page
	pagesDone bool
	pagesCut  bool // the tree had more than maxPages pages
	pagesErr  error
	title     string
	titleDone bool
}

// Open reads a PDF's cross-reference data and trailer from r, which holds size
// bytes. A file whose cross-reference data is missing or damaged is rebuilt by
// scanning it for objects.
func Open(r io.ReaderAt, size int64) (*Doc, error) {
	if size <= 0 {
		return nil, errors.New("pdf: empty file")
	}
	d := &Doc{
		r: r, size: size,
		objs: map[uint32]any{}, busy: map[uint32]bool{},
		budget: maxDecoded, lexLeft: maxLexed + 4*min(size, 1<<40), ops: maxOps,
		fonts: map[uint32]*font{}, cmaps: map[uint32]*cmap{}, inlineFn: map[uintptr]inlineFont{},
		tableLeft: maxTables,
	}
	head := make([]byte, min(size, 1024))
	n, _ := r.ReadAt(head, 0)
	if !bytes.Contains(head[:n], []byte("%PDF-")) {
		return nil, errors.New("pdf: no %PDF- header")
	}
	if err := d.loadXref(); err != nil || d.trailer["Root"] == nil {
		d.useScan()
	}
	d.setupSecurity()
	if !isCatalog(d.catalog()) {
		d.useScan()
		d.setupSecurity()
		// Without a catalog, a file still opens if it is locked, so Text can say
		// why there is nothing to read, or if pages survive to be read on their
		// own, as they do in a truncated file.
		if !isCatalog(d.catalog()) && !d.findCatalog() && !d.locked && !d.orphansLikely() {
			return nil, errors.New("pdf: no document catalog")
		}
	}
	return d, nil
}

func (d *Doc) catalog() dict { return d.dictOf(d.trailer["Root"]) }

// setupSecurity reads the trailer's /Encrypt, if any, and either sets up
// decryption or marks the document locked.
func (d *Doc) setupSecurity() {
	d.sec, d.locked, d.plainMeta = nil, false, false
	ev, ok := d.trailer["Encrypt"]
	if !ok || ev == nil {
		return
	}
	var encNum uint32
	if r, ok := ev.(ref); ok {
		encNum = r.num
	}
	enc := d.dictOf(ev)
	var id0 []byte
	if ids, ok := d.resolve(d.trailer["ID"]).(array); ok && len(ids) > 0 {
		s, _ := d.resolve(ids[0]).(string)
		id0 = []byte(s)
	}
	sec, err := newSecurity(enc, id0)
	if err != nil {
		d.locked = true
		if b, ok := enc["EncryptMetadata"].(bool); ok && !b {
			d.plainMeta = true
		}
		return
	}
	sec.encNum = encNum
	d.sec = sec
	// Objects read so far were read without decryption.
	clear(d.objs)
	d.stms = stmCache{}
	if d.scan != nil {
		d.scan.indexed, d.scan.packed = false, nil
	}
}

// Pages returns the number of pages in the page tree, or in a damaged file whose
// tree is gone, the number of pages a scan of the file finds.
func (d *Doc) Pages() int {
	pages, _ := d.pageList()
	n := len(pages)
	if d.pagesCut {
		if c, ok := d.intOf(d.dictOf(d.catalog()["Pages"])["Count"]); ok && c > int64(n) && c < 1<<31 {
			n = int(c)
		}
	}
	return n
}

// Text returns the text of the pages in order: a newline where a line of text
// ends, a space between words, and a newline between pages. It stops after
// limit runes; limit <= 0 means 2,000,000. A page whose content cannot be read is
// skipped.
func (d *Doc) Text(limit int) (string, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	if d.locked {
		return "", ErrEncrypted
	}
	pages, err := d.pageList()
	if err != nil {
		return "", err
	}
	out := &textOut{limit: limit}
	x := &interp{d: d, out: out}
	for i, p := range pages {
		if i >= maxTextPages || out.full || d.ops <= 0 {
			break
		}
		x.page(p)
		out.sep('\n')
	}
	return out.String(), nil
}

// page is one leaf of the page tree, with the resources it inherits.
type page struct {
	d   dict
	res dict
}

// pageList lists the pages in order. When the page tree yields none, as in a
// truncated file, whose tree is written last, it collects the pages left behind.
func (d *Doc) pageList() ([]page, error) {
	if d.pagesDone {
		return d.pages, d.pagesErr
	}
	d.pagesDone = true
	root, ok := d.catalog()["Pages"]
	if ok {
		d.walkPages(root)
	}
	if len(d.pages) == 0 && !d.locked {
		d.orphanPages()
	}
	if !ok && len(d.pages) == 0 {
		d.pagesErr = errors.New("pdf: no page tree")
	}
	return d.pages, d.pagesErr
}

// walkPages walks the page tree in order. References already visited are not
// followed again, so a tree that loops back on itself ends.
func (d *Doc) walkPages(root any) {
	type frame struct {
		kids array
		i    int
		res  dict
	}
	stack := []frame{{kids: array{root}}}
	seen := map[uint32]bool{}
	for visits := 0; len(stack) > 0 && visits < 4*maxPages; {
		top := &stack[len(stack)-1]
		if top.i >= len(top.kids) {
			stack = stack[:len(stack)-1]
			continue
		}
		node, res := top.kids[top.i], top.res
		top.i++
		visits++
		if r, ok := node.(ref); ok {
			if seen[r.num] {
				continue
			}
			seen[r.num] = true
		}
		nd := d.dictOf(node)
		if nd == nil {
			continue
		}
		if rd := d.dictOf(nd["Resources"]); rd != nil {
			res = rd
		}
		typ, _ := nd["Type"].(name)
		kids, isNode := d.resolve(nd["Kids"]).(array)
		switch {
		case typ == "Page" || !isNode && typ != "Pages":
			if len(d.pages) >= maxPages {
				d.pagesCut = true
				return
			}
			d.pages = append(d.pages, page{nd, res})
		case isNode && len(stack) < maxDepth:
			stack = append(stack, frame{kids: kids, res: res})
		}
	}
}

// orphansLikely reports whether a file without a catalog may still hold pages:
// the scan saw a page object, or an object stream that may pack some.
func (d *Doc) orphansLikely() bool {
	s := d.scanTable()
	if len(s.pages) > 0 {
		return true
	}
	if !s.indexed {
		d.indexObjStms(s)
	}
	for _, n := range s.packed {
		if t, _ := d.dictOf(ref{num: n})["Type"].(name); t == "Page" {
			return true
		}
	}
	return false
}

// orphanPages collects every object that is a page, in file order, then those
// packed in object streams. Each takes the resources of the nearest ancestor
// still there when it has none of its own.
func (d *Doc) orphanPages() {
	s := d.scanTable()
	seen := map[uint32]bool{}
	add := func(num uint32) bool {
		if seen[num] {
			return true
		}
		seen[num] = true
		pd := d.dictOf(ref{num: num})
		if t, _ := pd["Type"].(name); t != "Page" {
			return true
		}
		if len(d.pages) >= maxPages {
			d.pagesCut = true
			return false
		}
		var res dict
		for node, up := pd, 0; node != nil && up < 16; node, up = d.dictOf(node["Parent"]), up+1 {
			if res = d.dictOf(node["Resources"]); res != nil {
				break
			}
		}
		d.pages = append(d.pages, page{pd, res})
		return true
	}
	for _, n := range s.pages {
		if !add(n) {
			return
		}
	}
	if !s.indexed {
		d.indexObjStms(s)
	}
	for _, n := range s.packed {
		if !add(n) {
			return
		}
	}
}
