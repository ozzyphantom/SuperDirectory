package sniff

import (
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"strings"
)

const (
	oleEntries   = 4096       // the most directory entries read
	oleDIFAT     = 1024       // the most DIFAT sectors read: enough to place the FAT of an 8 GiB file
	oleMaxSector = 0xfffffffa // sector numbers above this are markers: free, end of chain
)

// oleType names an OLE2 compound file (the container of Office 97 to 2003
// documents, Outlook messages and Windows installers) by the streams its root
// storage holds. Word keeps a document in WordDocument, Excel in Workbook (Book
// before Excel 97), PowerPoint in "PowerPoint Document", and Outlook a message's
// properties in __substg1.0_ streams. Only the root's own children count: a Word
// document with a workbook embedded keeps the workbook's streams a storage down.
//
// It reads the header, then the directory a sector at a time, following the FAT
// that chains the sectors together. The walk reads at most 4096 entries and
// stops at any entry or sector it has seen before, so a looping file ends.
func oleType(r io.ReaderAt, size int64) (string, error) {
	var h [512]byte
	if err := readAt(r, size, h[:], 0); err != nil {
		return failed("ole", err)
	}
	shift := uint(binary.LittleEndian.Uint16(h[0x1e:]))
	if binary.LittleEndian.Uint16(h[0x1c:]) != 0xfffe || (shift != 9 && shift != 12) {
		return "ole", nil // not little-endian, or sectors of neither 512 bytes nor 4 KiB
	}
	c := &compound{
		r: r, size: size, shift: shift,
		dirStart:  binary.LittleEndian.Uint32(h[0x30:]),
		nextDIFAT: binary.LittleEndian.Uint32(h[0x44:]),
		difatSeen: map[uint32]bool{},
	}
	for i := 0; i < 109; i++ {
		c.fat = append(c.fat, binary.LittleEndian.Uint32(h[0x4c+4*i:]))
	}
	root, err := c.entry(0)
	if err != nil {
		return failed("ole", err)
	}
	if root[0x42] != 5 { // the root storage
		return "ole", nil
	}

	var doc, xls, ppt, msg bool
	seen := map[uint32]bool{0: true}
	stack := []uint32{binary.LittleEndian.Uint32(root[0x4c:])} // the root's children form a tree
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id >= oleEntries || seen[id] {
			continue // no entry (0xFFFFFFFF), past the cap, or a loop
		}
		seen[id] = true
		e, err := c.entry(id)
		if errors.Is(err, errShort) {
			continue
		}
		if err != nil {
			return "", err
		}
		if t := e[0x42]; t != 1 && t != 2 { // neither storage nor stream
			continue
		}
		name := entryName(e)
		switch {
		case strings.EqualFold(name, "WordDocument"):
			doc = true
		case strings.EqualFold(name, "Workbook"), strings.EqualFold(name, "Book"):
			xls = true
		case strings.EqualFold(name, "PowerPoint Document"):
			ppt = true
		case strings.HasPrefix(strings.ToLower(name), "__substg1.0_"):
			msg = true
		}
		stack = append(stack, binary.LittleEndian.Uint32(e[0x44:]), binary.LittleEndian.Uint32(e[0x48:]))
	}
	switch {
	case doc:
		return "doc", nil
	case xls:
		return "xls", nil
	case ppt:
		return "ppt", nil
	case msg:
		return "msg", nil
	}
	return "ole", nil
}

// compound reads the sectors of a compound file. Sector n starts at byte
// (n+1) << shift: the header takes the place of sector -1.
type compound struct {
	r     io.ReaderAt
	size  int64
	shift uint

	fat       []uint32 // where each FAT sector is: the header's 109, then the DIFAT's
	nextDIFAT uint32   // the next DIFAT sector, which lists where more FAT sectors are
	difatSeen map[uint32]bool

	dirStart uint32
	dir      []uint32 // the directory's sectors, in chain order, as far as followed
	dirEnded bool     // the chain ended or broke: no more sectors follow
}

// at returns where sector s starts, and false when s is a marker rather than a
// sector, or lies beyond the file.
func (c *compound) at(s uint32) (int64, bool) {
	off := (int64(s) + 1) << c.shift
	return off, s <= oleMaxSector && off < c.size
}

// next returns the sector after s in its chain, as the FAT records it.
func (c *compound) next(s uint32) (uint32, error) {
	per := uint32(1) << (c.shift - 2) // FAT entries in a sector
	f, err := c.fatSector(s / per)
	if err != nil {
		return 0, err
	}
	off, ok := c.at(f)
	if !ok {
		return 0, errShort
	}
	var b [4]byte
	if err := readAt(c.r, c.size, b[:], off+int64(s%per)*4); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[:]), nil
}

// fatSector returns where the i-th sector of the FAT is, reading DIFAT sectors
// as far as it must.
func (c *compound) fatSector(i uint32) (uint32, error) {
	for uint64(len(c.fat)) <= uint64(i) {
		off, ok := c.at(c.nextDIFAT)
		if !ok || c.difatSeen[c.nextDIFAT] || len(c.difatSeen) >= oleDIFAT {
			return 0, errShort
		}
		c.difatSeen[c.nextDIFAT] = true
		b := make([]byte, 1<<c.shift)
		if err := readAt(c.r, c.size, b, off); err != nil {
			return 0, err
		}
		last := len(b) - 4 // the last slot links to the next DIFAT sector
		for j := 0; j < last; j += 4 {
			c.fat = append(c.fat, binary.LittleEndian.Uint32(b[j:]))
		}
		c.nextDIFAT = binary.LittleEndian.Uint32(b[last:])
	}
	return c.fat[i], nil
}

// entry reads directory entry id, following the directory's chain of sectors as
// far as it must. The chain is cut at the first sector it repeats.
func (c *compound) entry(id uint32) ([]byte, error) {
	per := uint32(1) << (c.shift - 7) // directory entries in a sector
	k := id / per
	for uint64(len(c.dir)) <= uint64(k) {
		if c.dirEnded {
			return nil, errShort
		}
		s := c.dirStart
		if len(c.dir) > 0 {
			var err error
			if s, err = c.next(c.dir[len(c.dir)-1]); err != nil {
				if errors.Is(err, errShort) {
					c.dirEnded = true
				}
				return nil, err
			}
		}
		if _, ok := c.at(s); !ok || slices.Contains(c.dir, s) {
			c.dirEnded = true
			return nil, errShort
		}
		c.dir = append(c.dir, s)
	}
	off, _ := c.at(c.dir[k])
	e := make([]byte, 128)
	if err := readAt(c.r, c.size, e, off+int64(id%per)*128); err != nil {
		return nil, err
	}
	return e, nil
}

// entryName decodes a directory entry's name: UTF-16, with its length in bytes
// counting the terminating NUL. A character outside ASCII becomes '?', which
// none of the names looked for contain.
func entryName(e []byte) string {
	n := int(binary.LittleEndian.Uint16(e[0x40:]))/2 - 1 // characters, less the NUL
	if n < 1 || n > 31 {
		return ""
	}
	var b strings.Builder
	for i := range n {
		u := binary.LittleEndian.Uint16(e[2*i:])
		if u == 0 {
			break
		}
		if u > 0x7e {
			u = '?'
		}
		b.WriteByte(byte(u))
	}
	return b.String()
}
