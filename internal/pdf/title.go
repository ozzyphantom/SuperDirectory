package pdf

import (
	"bytes"
	"encoding/xml"
	"strings"
	"unicode"
)

// Title returns the document's title: the Info dictionary's /Title, or when that
// is absent or empty, the dc:title of the XMP metadata. It returns "" when there
// is none, and when the document is encrypted with a password; Text reports
// ErrEncrypted for that. Unencrypted metadata in a locked file is still read.
func (d *Doc) Title() string {
	if !d.titleDone {
		d.titleDone = true
		d.title = d.findTitle()
	}
	return d.title
}

func (d *Doc) findTitle() string {
	if d.locked && !d.plainMeta {
		return ""
	}
	if !d.locked {
		info := d.dictOf(d.trailer["Info"])
		if s, ok := d.resolve(info["Title"]).(string); ok {
			if t := cleanTitle(textString(s)); t != "" {
				return t
			}
		}
	}
	md, ok := d.resolve(d.catalog()["Metadata"]).(*stream)
	if !ok {
		return ""
	}
	data, err := d.decode(md)
	if err != nil {
		return ""
	}
	return cleanTitle(xmpTitle(data))
}

// cleanTitle trims white space and NULs, which pad many titles, and spells out
// ligatures.
func cleanTitle(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.TrimFunc(s, unicode.IsSpace)
	if strings.ContainsFunc(s, func(r rune) bool { return r >= 0xFB00 && r <= 0xFB06 }) {
		var b strings.Builder
		for _, r := range s {
			if r >= 0xFB00 && r <= 0xFB06 {
				b.WriteString(ligatures[r-0xFB00])
			} else {
				b.WriteRune(r)
			}
		}
		s = b.String()
	}
	return s
}

const (
	nsDC  = "http://purl.org/dc/elements/1.1/"
	nsRDF = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
	nsXML = "http://www.w3.org/XML/1998/namespace"
)

// xmpTitle returns the dc:title of an XMP packet: the rdf:li marked x-default, or
// else the first one. A title written as plain text inside dc:title is taken
// too. Namespace prefixes left undeclared are matched by name.
func xmpTitle(data []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	is := func(n xml.Name, ns, prefix, local string) bool {
		return n.Local == local && (n.Space == ns || n.Space == prefix)
	}
	var (
		inTitle  int // element depth inside dc:title, 0 when outside
		inLi     bool
		lang     string
		text     strings.Builder
		first    string
		haveLi   bool
		direct   strings.Builder // text directly inside dc:title
		tokCount int
	)
	for tokCount < 1_000_000 {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		tokCount++
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case inTitle == 0 && is(t.Name, nsDC, "dc", "title"):
				inTitle = 1
			case inTitle > 0:
				inTitle++
				if is(t.Name, nsRDF, "rdf", "li") {
					inLi, lang = true, ""
					text.Reset()
					for _, a := range t.Attr {
						if is(a.Name, nsXML, "xml", "lang") {
							lang = a.Value
						}
					}
				}
			}
		case xml.CharData:
			switch {
			case inLi:
				text.Write(t)
			case inTitle == 1:
				direct.Write(t)
			}
		case xml.EndElement:
			if inTitle == 0 {
				continue
			}
			if inLi && is(t.Name, nsRDF, "rdf", "li") {
				inLi = false
				if strings.EqualFold(lang, "x-default") {
					return text.String()
				}
				if !haveLi {
					first, haveLi = text.String(), true
				}
			}
			if inTitle--; inTitle == 0 {
				if haveLi {
					return first
				}
				return direct.String()
			}
		}
	}
	return first
}
