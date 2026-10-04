package textual

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
)

// epub reads an e-book's chapters in reading order. container.xml names the
// package document; its spine lists the chapters, each an XHTML page read the
// way any page is. The book's title opens the text once; the chapters' own
// <title>s, which mostly repeat it, are left out.
func epub(r io.ReaderAt, size int64, limit int) (string, error) {
	z, err := openZip(r, size)
	if err != nil {
		return "", fmt.Errorf("epub: %w", err)
	}
	files := entries(z)

	src, err := readEntry(files, "META-INF/container.xml")
	if err != nil && len(src) == 0 {
		return "", fmt.Errorf("epub: %w", err)
	}
	var container struct {
		Rootfiles []struct {
			Path string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := xmlDecoder(src).Decode(&container); err != nil && len(container.Rootfiles) == 0 {
		return "", fmt.Errorf("epub: container.xml: %w", err)
	}
	if len(container.Rootfiles) == 0 || container.Rootfiles[0].Path == "" {
		return "", errors.New("epub: container.xml names no package document")
	}
	opfPath := container.Rootfiles[0].Path

	src, err = readEntry(files, opfPath)
	if err != nil && len(src) == 0 {
		return "", fmt.Errorf("epub: %w", err)
	}
	var pkg struct {
		Titles []string `xml:"metadata>title"`
		Items  []struct {
			ID   string `xml:"id,attr"`
			Href string `xml:"href,attr"`
			Type string `xml:"media-type,attr"`
		} `xml:"manifest>item"`
		Spine []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"spine>itemref"`
	}
	if err := xmlDecoder(src).Decode(&pkg); err != nil && len(pkg.Spine) == 0 {
		return "", fmt.Errorf("epub: %s: %w", opfPath, err)
	}

	o := newBuilder(limit)
	for _, t := range pkg.Titles {
		if t = strings.TrimSpace(t); t != "" {
			o.title(t)
			break
		}
	}

	hrefs := map[string]string{}
	for _, it := range pkg.Items {
		if isPage(it.Href, it.Type) {
			hrefs[it.ID] = it.Href
		}
	}
	dir := path.Dir(opfPath)
	seen := map[string]bool{}
	for _, ref := range pkg.Spine {
		if o.full() {
			break
		}
		href, ok := hrefs[ref.IDRef]
		if !ok {
			continue // a picture or a stylesheet in the spine, or a broken reference
		}
		name := resolve(dir, href)
		if seen[name] {
			continue
		}
		seen[name] = true
		page, _ := readEntry(files, name)
		if len(page) == 0 {
			continue // one missing chapter should not cost the book
		}
		o.block(2)
		renderHTML(o, decode(page, len(page) >= maxEntry), false)
	}
	return o.String(), nil
}

// isPage reports whether a manifest item is a page of text.
func isPage(href, mediaType string) bool {
	if strings.Contains(strings.ToLower(mediaType), "html") {
		return true
	}
	name, _, _ := strings.Cut(href, "#")
	switch extension(name) {
	case "xhtml", "html", "htm":
		return true
	}
	return false
}

// resolve turns a manifest href, which is a URL relative to the package
// document, into a name inside the archive.
func resolve(dir, href string) string {
	href, _, _ = strings.Cut(href, "#")
	if u, err := url.PathUnescape(href); err == nil {
		href = u
	}
	if strings.HasPrefix(href, "/") {
		return path.Clean(strings.TrimPrefix(href, "/"))
	}
	return path.Join(dir, href)
}
