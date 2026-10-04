// Package chm reads Microsoft Compiled HTML Help files.
//
// A .chm is an ITSF archive: a directory of named entries over two content
// sections. Section 0 stores entries as they are. Section 1, MSCompressed, is
// one LZX stream, and every compressed entry is a slice of it. Old vendor
// documentation often ships this way; expanding it gives back the pages and
// images as ordinary files.
//
// The bytes come from arbitrary files. Every offset is checked, every
// allocation driven by the file is capped, every loop is bounded, and
// malformed input yields an error, never a crash.
package chm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"strings"
	"sync"
)

// Entry is one file in the archive.
type Entry struct {
	Name string // path inside the archive, without the leading "/", e.g. "html/setup.htm"
	Size int64
}

// File is an open archive. It is safe for concurrent use.
type File struct {
	r     io.ReaderAt
	size  int64
	lim   limits
	sec0  int64            // archive offset of content section 0
	files []entry          // user files, in archive order
	index map[string]int   // name to position in files
	sys   map[string]entry // the ::DataSpace files the compressed section needs

	mu     sync.Mutex // guards the compressed section, whose decoder carries state
	lzx    *lzxSection
	lzxErr error
}

type entry struct {
	Entry
	section int
	offset  int64
}

// limits caps what a hostile archive can make this package allocate or do.
type limits struct {
	entries int   // directory entries, the archive's own included
	name    int   // bytes in one entry's name
	entry   int64 // bytes in one entry
	total   int64 // bytes Extract writes in all
	stream  int64 // decoded length of the compressed section
}

var defaultLimits = limits{
	entries: 200_000,
	name:    1024,
	entry:   256 << 20,
	total:   2 << 30,
	stream:  8 << 30,
}

const (
	itsfV2Len    = 0x58 // the ITSF header of version 2; version 3 adds 8 bytes
	itsfV3Len    = 0x60
	itspLen      = 0x54 // the directory header
	pmglHeader   = 0x14 // a listing chunk's header
	minChunkSize = pmglHeader + 2
	maxChunkSize = 8192 // chmd.c's limit; real archives use 4096
	maxChunks    = 100_000
	noChunk      = 0xFFFFFFFF
)

var errPastEnd = errors.New("it runs past the end of the file")

// Open reads an archive's headers and directory. Entries are read later, on
// demand, so opening even a large archive reads only its directory.
func Open(r io.ReaderAt, size int64) (*File, error) {
	return open(r, size, defaultLimits)
}

func open(r io.ReaderAt, size int64, lim limits) (*File, error) {
	var h [itsfV3Len]byte
	if err := readAt(r, size, 0, h[:itsfV2Len]); err != nil {
		return nil, fmt.Errorf("chm: header: %w", err)
	}
	if string(h[:4]) != "ITSF" {
		return nil, errors.New("chm: not a CHM file: no ITSF signature")
	}
	version := binary.LittleEndian.Uint32(h[4:])
	if version != 2 && version != 3 {
		return nil, fmt.Errorf("chm: ITSF version %d; only 2 and 3 are known", version)
	}
	// The header section table: section 0 records the file's length, which this
	// reader takes from size instead; section 1 is the directory.
	dirOff, ok := off64(h[0x48:])
	if !ok {
		return nil, errors.New("chm: the directory offset is out of range")
	}

	var d [itspLen]byte
	if err := readAt(r, size, dirOff, d[:]); err != nil {
		return nil, fmt.Errorf("chm: directory header: %w", err)
	}
	if string(d[:4]) != "ITSP" {
		return nil, errors.New("chm: no ITSP signature on the directory")
	}
	if n := binary.LittleEndian.Uint32(d[8:]); n != itspLen {
		return nil, fmt.Errorf("chm: a directory header of %d bytes; it should be %d", n, itspLen)
	}
	chunkSize := int64(binary.LittleEndian.Uint32(d[0x10:]))
	firstPMGL := binary.LittleEndian.Uint32(d[0x20:])
	numChunks := int64(binary.LittleEndian.Uint32(d[0x2C:]))
	if chunkSize < minChunkSize || chunkSize > maxChunkSize {
		return nil, fmt.Errorf("chm: directory chunks of %d bytes; %d to %d are plausible", chunkSize, minChunkSize, maxChunkSize)
	}
	if numChunks == 0 || numChunks > maxChunks {
		return nil, fmt.Errorf("chm: %d directory chunks; 1 to %d are plausible", numChunks, maxChunks)
	}
	if firstPMGL == noChunk {
		return nil, errors.New("chm: the directory has no listing chunks")
	}
	chunks := dirOff + itspLen
	if chunks > size || chunkSize*numChunks > size-chunks {
		return nil, errors.New("chm: the directory runs past the end of the file")
	}

	sec0 := chunks + chunkSize*numChunks // version 2: right after the directory
	if version == 3 {
		if err := readAt(r, size, itsfV2Len, h[itsfV2Len:]); err != nil {
			return nil, fmt.Errorf("chm: header: %w", err)
		}
		if sec0, ok = off64(h[itsfV2Len:]); !ok || sec0 > size {
			return nil, errors.New("chm: content section 0 begins past the end of the file")
		}
	}

	f := &File{
		r:     r,
		size:  size,
		lim:   lim,
		sec0:  sec0,
		index: make(map[string]int),
		sys:   make(map[string]entry),
	}
	// Walk the listing chunks in order, by their links. The index chunks only
	// speed up lookups by name, which a full listing does not need.
	seen := make([]bool, numChunks)
	chunk := make([]byte, chunkSize)
	count := 0
	for c := firstPMGL; c != noChunk; c = binary.LittleEndian.Uint32(chunk[0x10:]) {
		if int64(c) >= numChunks {
			return nil, fmt.Errorf("chm: the directory links to chunk %d of %d", c, numChunks)
		}
		if seen[c] {
			return nil, fmt.Errorf("chm: the directory's chunks loop back to chunk %d", c)
		}
		seen[c] = true
		if err := readAt(r, size, chunks+int64(c)*chunkSize, chunk); err != nil {
			return nil, fmt.Errorf("chm: directory chunk %d: %w", c, err)
		}
		if string(chunk[:4]) != "PMGL" {
			return nil, fmt.Errorf("chm: directory chunk %d is not a listing chunk", c)
		}
		if err := f.parseChunk(chunk, &count); err != nil {
			return nil, fmt.Errorf("chm: directory chunk %d: %w", c, err)
		}
	}
	return f, nil
}

// parseChunk reads a listing chunk's entries. Their count is in the chunk's
// last two bytes, as chmd.c reads it.
func (f *File) parseChunk(chunk []byte, count *int) error {
	end := len(chunk) - 2
	n := int(binary.LittleEndian.Uint16(chunk[end:]))
	body := chunk[:end]
	p := pmglHeader
	for i := range n {
		nameLen, ok := encint(body, &p)
		if !ok || nameLen > int64(end-p) {
			return fmt.Errorf("entry %d runs past the end of the chunk", i)
		}
		if nameLen > int64(f.lim.name) {
			return fmt.Errorf("entry %d has a name of %d bytes; the limit is %d", i, nameLen, f.lim.name)
		}
		name := string(body[p : p+int(nameLen)])
		p += int(nameLen)
		section, ok1 := encint(body, &p)
		offset, ok2 := encint(body, &p)
		length, ok3 := encint(body, &p)
		if !ok1 || !ok2 || !ok3 {
			return fmt.Errorf("entry %q runs past the end of the chunk", name)
		}
		*count++
		if *count > f.lim.entries {
			return fmt.Errorf("more than %d entries", f.lim.entries)
		}
		f.add(name, section, offset, length)
	}
	return nil
}

// encint reads the directory's variable-length integer: big-endian groups of
// seven bits, the high bit set on every byte but the last. Nine bytes hold 63
// bits; a longer one is malformed.
func encint(b []byte, p *int) (int64, bool) {
	var v int64
	for range 9 {
		if *p >= len(b) {
			return 0, false
		}
		c := b[*p]
		*p++
		v = v<<7 | int64(c&0x7F)
		if c&0x80 == 0 {
			return v, true
		}
	}
	return 0, false
}

// add files one directory entry: the archive's own files the compressed
// section needs, or a user file. Directories, the archive's other bookkeeping
// and sections past 1 are dropped.
func (f *File) add(raw string, section, offset, length int64) {
	if sysNames[raw] {
		if _, dup := f.sys[raw]; !dup {
			f.sys[raw] = entry{Entry{raw, length}, int(min(section, 2)), offset}
		}
		return
	}
	if section > 1 {
		return // a section this reader does not know; chmd.c skips these too
	}
	name := strings.TrimPrefix(raw, "/")
	if name == "" || strings.HasSuffix(name, "/") ||
		strings.HasPrefix(name, "::") || strings.HasPrefix(name, "#") || strings.HasPrefix(name, "$") {
		return
	}
	if _, dup := f.index[name]; dup {
		return // a name listed twice keeps its first entry
	}
	f.index[name] = len(f.files)
	f.files = append(f.files, entry{Entry{name, length}, int(section), offset})
}

// Entries lists the archive's files in archive order: the pages, images and
// other files a reader of the help sees. It leaves out directories and the
// archive's own bookkeeping, whose names start with "::", "#" or "$".
func (f *File) Entries() []Entry {
	out := make([]Entry, len(f.files))
	for i, e := range f.files {
		out[i] = e.Entry
	}
	return out
}

// ReadFile returns the contents of the entry Entries lists under name. A
// compressed entry is decoded from the reset point nearest before it, so
// reading one file never decompresses the whole archive.
func (f *File) ReadFile(name string) ([]byte, error) {
	i, ok := f.index[name]
	if !ok {
		return nil, fmt.Errorf("chm: %s: %w", name, fs.ErrNotExist)
	}
	b, err := f.read(&f.files[i])
	if err != nil {
		return nil, fmt.Errorf("chm: %s: %w", name, err)
	}
	return b, nil
}

func (f *File) read(e *entry) ([]byte, error) {
	if e.Size > f.lim.entry {
		return nil, fmt.Errorf("%d bytes is more than the %d one entry may hold", e.Size, f.lim.entry)
	}
	if e.Size == 0 {
		return []byte{}, nil
	}
	if e.section == 0 {
		if e.offset > f.size-f.sec0 || e.Size > f.size-f.sec0-e.offset {
			return nil, errPastEnd
		}
		b := make([]byte, e.Size)
		if err := readAt(f.r, f.size, f.sec0+e.offset, b); err != nil {
			return nil, err
		}
		return b, nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	s, err := f.compressed()
	if err != nil {
		return nil, err
	}
	if e.offset > s.length || e.Size > s.length-e.offset {
		return nil, errors.New("it runs past the end of the compressed section")
	}
	return s.read(e.offset, e.Size)
}

// compressed returns the compressed section, reading its description on first
// use. The caller holds f.mu.
func (f *File) compressed() (*lzxSection, error) {
	if f.lzx == nil && f.lzxErr == nil {
		if f.lzx, f.lzxErr = f.loadCompressed(); f.lzxErr != nil {
			f.lzxErr = fmt.Errorf("compressed section: %w", f.lzxErr)
		}
	}
	return f.lzx, f.lzxErr
}

// readAt fills p from the archive at off, refusing to read outside it.
func readAt(r io.ReaderAt, size, off int64, p []byte) error {
	if off < 0 || off > size || int64(len(p)) > size-off {
		return errPastEnd
	}
	n, err := r.ReadAt(p, off)
	if n == len(p) {
		return nil
	}
	if err == nil || errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// off64 reads an unsigned 64-bit offset, which must fit an int64.
func off64(b []byte) (int64, bool) {
	v := binary.LittleEndian.Uint64(b)
	return int64(v), v <= math.MaxInt64
}
