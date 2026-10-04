package title

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"io"
	"slices"
	"strings"
)

const (
	// dirBudget bounds the bytes read to open an archive: the search for its end
	// record, up to 66 KiB, and its central directory. archive/zip allocates for
	// every entry it reads, so the budget bounds its memory too, to about 5 MB for
	// a hostile directory of empty names. A real document's directory takes a few
	// KiB; an EPUB with thousands of images, a few hundred.
	dirBudget = 1<<20 + 66<<10
	// entryBudget bounds the raw bytes read for one entry: its local header, up to
	// entryLimit of data, and the decompressor's read-ahead.
	entryBudget = entryLimit + 64<<10
	// maxDepth bounds the nesting followed in a metadata part. Real ones nest
	// three or four elements deep.
	maxDepth = 64
)

// The namespaces of the elements that hold a title.
const (
	dcNS     = "http://purl.org/dc/elements/1.1/"
	dcOldNS  = "http://purl.org/dc/elements/1.0/" // Open eBook, before EPUB
	officeNS = "urn:oasis:names:tc:opendocument:xmlns:office:1.0"
)

var officeMeta = xml.Name{Space: officeNS, Local: "meta"}

// isDC reports whether space is Dublin Core's. An e-book that forgot to declare
// the dc prefix is left with the prefix itself.
func isDC(space string) bool { return space == dcNS || space == dcOldNS || space == "dc" }

// ooxmlFile reads an Office Open XML file's title: dc:title in its core
// properties, docProps/core.xml.
func ooxmlFile(r io.ReaderAt, size int64) string {
	a := openZip(r, size)
	if a == nil {
		return ""
	}
	return a.text("docProps/core.xml", func(anc []xml.Name, el xml.Name) bool {
		return len(anc) == 1 && isDC(el.Space) && el.Local == "title"
	})
}

// odfFile reads an OpenDocument file's title: dc:title in office:meta, in
// meta.xml.
func odfFile(r io.ReaderAt, size int64) string {
	a := openZip(r, size)
	if a == nil {
		return ""
	}
	return a.text("meta.xml", func(anc []xml.Name, el xml.Name) bool {
		return len(anc) > 0 && anc[len(anc)-1] == officeMeta && isDC(el.Space) && el.Local == "title"
	})
}

// epubFile reads an EPUB's title: the first dc:title in the metadata of its
// package document, which META-INF/container.xml locates. An e-book can carry
// several titles, a subtitle among them; the first is the main one.
func epubFile(r io.ReaderAt, size int64) string {
	a := openZip(r, size)
	if a == nil {
		return ""
	}
	opf := a.rootfile()
	if opf == "" {
		return ""
	}
	return a.text(opf, func(anc []xml.Name, el xml.Name) bool {
		return isDC(el.Space) && strings.EqualFold(el.Local, "title") &&
			slices.ContainsFunc(anc, func(n xml.Name) bool { return n.Local == "metadata" })
	})
}

// archive is a ZIP file opened under a read budget.
type archive struct {
	zip    *zip.Reader
	budget *budget
}

// openZip reads a ZIP archive's directory, or returns nil when r holds none.
func openZip(r io.ReaderAt, size int64) *archive {
	b := &budget{r: r, left: dirBudget}
	z, err := zip.NewReader(b, size)
	if z == nil || err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return nil // ErrInsecurePath still comes with a reader; nothing is extracted here
	}
	return &archive{zip: z, budget: b}
}

// read opens the named entry and hands fn at most its first entryLimit bytes,
// decompressed: a zip bomb inflates no further. A missing or unreadable entry
// never reaches fn.
func (a *archive) read(name string, fn func(io.Reader)) {
	f := a.find(name)
	if f == nil {
		return
	}
	a.budget.left = entryBudget
	rc, err := f.Open()
	if err != nil {
		return
	}
	defer rc.Close()
	fn(io.LimitReader(rc, entryLimit))
}

// find returns the entry named name. When no name matches exactly, it takes one
// that differs only in case: e-book readers forgive a container that spells the
// package document's path in another case, so e-books ship that way.
func (a *archive) find(name string) *zip.File {
	var fold *zip.File
	for _, f := range a.zip.File {
		if f.Name == name {
			return f
		}
		if fold == nil && strings.EqualFold(f.Name, name) {
			fold = f
		}
	}
	return fold
}

// text returns the title held in the named XML entry: the text of the first
// element that match accepts and that has any. match sees the element's name and
// its ancestors' names, outermost first.
func (a *archive) text(name string, match func(anc []xml.Name, el xml.Name) bool) string {
	var title string
	a.read(name, func(r io.Reader) { title = xmlTitle(r, match) })
	return title
}

func xmlTitle(r io.Reader, match func(anc []xml.Name, el xml.Name) bool) string {
	d := newDecoder(r)
	var anc []xml.Name
	for {
		tok, err := d.Token()
		if err != nil {
			return ""
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if match(anc, t.Name) {
				if s := normalize(elementText(d)); s != "" {
					return clean(s)
				}
				continue
			}
			if len(anc) == maxDepth {
				return ""
			}
			anc = append(anc, t.Name)
		case xml.EndElement:
			if len(anc) > 0 {
				anc = anc[:len(anc)-1]
			}
		}
	}
}

// elementText reads the rest of the element just opened and returns its
// character data, the text of any child elements included. A part that breaks off
// inside the element gives "".
func elementText(d *xml.Decoder) string {
	var b strings.Builder
	for depth := 1; depth > 0; {
		tok, err := d.Token()
		if err != nil {
			return ""
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		}
	}
	return b.String()
}

// rootfile returns the path of an EPUB's package document, from
// META-INF/container.xml: the first rootfile of the package media type, or else
// the first rootfile of any type.
func (a *archive) rootfile() string {
	var first, opf string
	a.read("META-INF/container.xml", func(r io.Reader) {
		d := newDecoder(r)
		for {
			tok, err := d.Token()
			if err != nil {
				return
			}
			el, ok := tok.(xml.StartElement)
			if !ok || el.Name.Local != "rootfile" {
				continue
			}
			var path, media string
			for _, at := range el.Attr {
				switch at.Name.Local {
				case "full-path":
					path = at.Value
				case "media-type":
					media = at.Value
				}
			}
			if path == "" {
				continue
			}
			if first == "" {
				first = path
			}
			if media == "application/oebps-package+xml" {
				opf = path
				return
			}
		}
	})
	if opf == "" {
		opf = first
	}
	return strings.TrimPrefix(opf, "/")
}

// newDecoder returns an XML decoder for a metadata part, set to forgive what
// hand-made and older files get wrong: an HTML entity such as &nbsp; that XML
// never declared, a bare '&', a legacy encoding.
func newDecoder(r io.Reader) *xml.Decoder {
	d := xml.NewDecoder(r)
	d.Strict = false
	d.Entity = xml.HTMLEntity
	d.CharsetReader = xmlCharset
	return d
}

// budget is an io.ReaderAt that stops after a set number of bytes. archive/zip
// believes an archive about the size of its own directory, and the budget is
// what stops a hostile one.
type budget struct {
	r    io.ReaderAt
	left int64
}

var errBudget = errors.New("title: read budget spent")

func (b *budget) ReadAt(p []byte, off int64) (int, error) {
	if b.left <= 0 {
		return 0, errBudget
	}
	short := int64(len(p)) > b.left
	if short {
		p = p[:b.left]
	}
	n, err := b.r.ReadAt(p, off)
	b.left -= int64(n)
	if err == nil && short {
		err = errBudget
	}
	return n, err
}
