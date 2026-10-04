package textual

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// docx reads a Word document's body. Headers, footers, footnotes and comments
// live in other parts of the package and are left out.
func docx(r io.ReaderAt, size int64, limit int) (string, error) {
	z, err := openZip(r, size)
	if err != nil {
		return "", fmt.Errorf("docx: %w", err)
	}
	src, err := readEntry(entries(z), "word/document.xml")
	if err != nil && len(src) == 0 {
		return "", fmt.Errorf("docx: %w", err)
	}
	o := newBuilder(limit)
	if xerr := wordText(o, src); err == nil {
		err = xerr
	}
	return o.String(), err
}

// wordText walks WordprocessingML. Text sits in w:t elements inside runs inside
// paragraphs; everything else is formatting, apart from the few elements that
// stand for a tab or a line break.
func wordText(o *builder, src []byte) error {
	d := xmlDecoder(src)
	var (
		inText  int  // depth inside w:t
		inTabs  int  // depth inside w:tabs, where w:tab defines a tab stop
		skip    int  // depth inside a part that duplicates or annotates the text
		heading bool // the paragraph is a heading
	)
	for !o.full() {
		tok, err := d.Token()
		if err != nil {
			return eofIsNil(err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			switch t.Name.Local {
			// A text box is stored twice, once for current Word and once as a
			// fallback for old readers; tracked changes keep deleted text and the
			// former place of moved text; a tracked formatting change keeps the
			// paragraph's former properties. Read any of them and text doubles
			// or a paragraph takes the wrong style.
			case "Fallback", "del", "moveFrom", "pPrChange", "rPrChange":
				skip = 1
			case "t":
				inText++
			case "tabs":
				inTabs++
			case "tab":
				if inTabs == 0 {
					o.raw("\t")
				}
			case "br", "cr":
				o.newline()
			case "noBreakHyphen":
				o.raw("-")
			case "pStyle":
				if n := wordHeading(attr(t, "val")); n > 0 {
					heading = true
					o.block(2)
					o.mark = strings.Repeat("#", n) + " "
				}
			case "numPr":
				if !heading {
					o.mark = "- "
				}
			case "tr":
				o.block(1)
			case "tc":
				o.cell()
				o.inline++
			}
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			switch t.Name.Local {
			case "t":
				inText = max(inText-1, 0)
			case "tabs":
				inTabs = max(inTabs-1, 0)
			case "p":
				o.mark = ""
				if heading {
					heading = false
					o.block(2)
				} else {
					o.block(1)
				}
			case "tc":
				o.inline = max(o.inline-1, 0)
			case "tr":
				o.block(1)
			}
		case xml.CharData:
			if inText > 0 && skip == 0 {
				o.text(string(t))
			}
		}
	}
	return nil
}

// wordHeading returns the heading level a paragraph style names, or 0. Word's
// built-in style IDs are "Heading1" through "Heading9" and "Title".
func wordHeading(style string) int {
	s := strings.ToLower(style)
	if s == "title" {
		return 1
	}
	rest, ok := strings.CutPrefix(s, "heading")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil || n < 1 || n > 9 {
		return 0
	}
	return min(n, 6)
}

// odt reads an OpenDocument text's body.
func odt(r io.ReaderAt, size int64, limit int) (string, error) {
	z, err := openZip(r, size)
	if err != nil {
		return "", fmt.Errorf("odt: %w", err)
	}
	src, err := readEntry(entries(z), "content.xml")
	if err != nil && len(src) == 0 {
		return "", fmt.Errorf("odt: %w", err)
	}
	o := newBuilder(limit)
	if xerr := openDocumentText(o, src); err == nil {
		err = xerr
	}
	return o.String(), err
}

// openDocumentText walks an ODF content.xml. Text counts only inside paragraphs
// and headings; the rest of the file is styles and declarations.
func openDocumentText(o *builder, src []byte) error {
	d := xmlDecoder(src)
	var (
		para  int // depth inside text:p and text:h
		lists int // depth of nested lists
		skip  int // depth inside notes, comments and tracked deletions
	)
	for !o.full() {
		tok, err := d.Token()
		if err != nil {
			return eofIsNil(err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			switch t.Name.Local {
			// A footnote's body sits inline, mid-sentence; a comment is about the
			// text, not in it; tracked changes keep deleted text.
			case "note", "annotation", "tracked-changes":
				skip = 1
			case "title", "desc":
				// A drawing's title and description, not a text field.
				if strings.Contains(t.Name.Space, "svg") {
					skip = 1
				}
			case "p":
				para++
			case "h":
				para++
				level, err := strconv.Atoi(attr(t, "outline-level"))
				if err != nil || level < 1 {
					level = 1
				}
				o.block(2)
				o.mark = strings.Repeat("#", min(level, 6)) + " "
			case "list":
				lists++
			case "list-item":
				o.block(1)
				o.mark = strings.Repeat("  ", max(lists-1, 0)) + "- "
			case "s":
				if para > 0 {
					n, err := strconv.Atoi(attr(t, "c"))
					if err != nil || n < 1 {
						n = 1
					}
					o.raw(strings.Repeat(" ", min(n, 64)))
				}
			case "tab":
				if para > 0 {
					o.raw("\t")
				}
			case "line-break":
				if para > 0 {
					o.newline()
				}
			case "table-row":
				o.block(1)
			case "table-cell":
				o.cell()
				o.inline++
			}
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			switch t.Name.Local {
			case "p":
				para = max(para-1, 0)
				o.mark = ""
				o.block(1)
			case "h":
				para = max(para-1, 0)
				o.mark = ""
				o.block(2)
			case "list":
				lists = max(lists-1, 0)
			case "table-cell":
				o.inline = max(o.inline-1, 0)
			case "table-row":
				o.block(1)
			}
		case xml.CharData:
			// ODF collapses whitespace in paragraphs, as HTML does; text:s spells
			// out the spaces that are meant.
			if para > 0 && skip == 0 {
				o.text(string(t))
			}
		}
	}
	return nil
}

// xmlDecoder returns a forgiving decoder over src. The bytes are converted to
// UTF-8 first, whatever the declaration says, so the declaration is not
// consulted again.
func xmlDecoder(src []byte) *xml.Decoder {
	d := xml.NewDecoder(strings.NewReader(decode(src, len(src) >= maxEntry)))
	d.Strict = false
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	return d
}

func attr(t xml.StartElement, local string) string {
	for _, a := range t.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func eofIsNil(err error) error {
	if err == io.EOF {
		return nil
	}
	return err
}

// openZip opens a package. An entry name that would escape a folder is no
// danger here, since nothing is written to disk, so the reader that comes
// with ErrInsecurePath is used like any other.
func openZip(r io.ReaderAt, size int64) (*zip.Reader, error) {
	z, err := zip.NewReader(r, size)
	if errors.Is(err, zip.ErrInsecurePath) && z != nil {
		err = nil
	}
	return z, err
}

// zipFiles finds a package's entries by name. Some tools write names with
// backslashes or in another case than the files that point to them, so a
// lookup falls back to a folded form.
type zipFiles struct {
	exact  map[string]*zip.File
	folded map[string]*zip.File
}

func entries(z *zip.Reader) zipFiles {
	f := zipFiles{exact: map[string]*zip.File{}, folded: map[string]*zip.File{}}
	for _, e := range z.File {
		if _, dup := f.exact[e.Name]; !dup {
			f.exact[e.Name] = e
		}
		k := fold(e.Name)
		if _, dup := f.folded[k]; !dup {
			f.folded[k] = e
		}
	}
	return f
}

func fold(name string) string {
	return strings.ToLower(strings.TrimPrefix(strings.ReplaceAll(name, `\`, "/"), "/"))
}

// readEntry reads one entry, capped at maxEntry bytes. A damaged entry yields
// what was read before the damage, with the error.
func readEntry(f zipFiles, name string) ([]byte, error) {
	e := f.exact[name]
	if e == nil {
		e = f.folded[fold(name)]
	}
	if e == nil {
		return nil, fmt.Errorf("no %s", name)
	}
	rc, err := e.Open()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxEntry))
	if err != nil {
		err = fmt.Errorf("%s: %w", name, err)
	}
	return b, err
}
