package pdf

import (
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"unicode/utf16"
)

// testPDF builds PDF files for tests. Objects are numbered from 1 in the order
// they are added; a stream's /Length is filled in when the file is written.
type testPDF struct {
	objs  []testObj
	crypt *testCrypt // nil: not encrypted
}

type testObj struct {
	body   string // the object, or a stream's dictionary entries
	data   []byte
	stream bool
	packed bool // goes into the object stream of a file written by modern
}

func (p *testPDF) add(body string) int {
	p.objs = append(p.objs, testObj{body: body})
	return len(p.objs)
}

// pack adds an object that modern writes into its object stream.
func (p *testPDF) pack(body string) int {
	p.objs = append(p.objs, testObj{body: body, packed: true})
	return len(p.objs)
}

func (p *testPDF) addStream(dict string, data []byte) int {
	p.objs = append(p.objs, testObj{body: dict, data: data, stream: true})
	return len(p.objs)
}

// next is the number the next object added will get.
func (p *testPDF) next() int { return len(p.objs) + 1 }

func (p *testPDF) writeObj(b *bytes.Buffer, num int, o testObj) {
	if !o.stream {
		fmt.Fprintf(b, "%d 0 obj\n%s\nendobj\n", num, o.body)
		return
	}
	data := o.data
	if p.crypt != nil {
		data = p.crypt.encrypt(num, data)
	}
	fmt.Fprintf(b, "%d 0 obj\n<< %s /Length %d >>\nstream\n", num, o.body, len(data))
	b.Write(data)
	b.WriteString("\nendstream\nendobj\n")
}

const header = "%PDF-1.7\n%\xe2\xe3\xcf\xd3\n"

// classic writes the file with a classic cross-reference table. The trailer gets
// /Size; trailer adds the rest, such as /Root.
func (p *testPDF) classic(trailer string) []byte {
	var b bytes.Buffer
	b.WriteString(header)
	offs := make([]int, len(p.objs)+1)
	for i, o := range p.objs {
		offs[i+1] = b.Len()
		p.writeObj(&b, i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(p.objs)+1)
	for _, off := range offs[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d %s >>\nstartxref\n%d\n%%%%EOF\n", len(p.objs)+1, trailer, xref)
	return b.Bytes()
}

// modern writes the packed objects into one object stream and indexes the file
// with a cross-reference stream, both Flate-compressed, the cross-reference
// stream through the PNG Up predictor (/Predictor 12).
func (p *testPDF) modern(trailer string) []byte {
	stmNum := len(p.objs) + 1
	xrefNum := stmNum + 1
	var b bytes.Buffer
	offs, index := p.writeBody(&b, stmNum)
	xrefOff := b.Len()
	offs[xrefNum] = xrefOff
	var rows []byte
	prev := make([]byte, 7)
	for num := 0; num <= xrefNum; num++ {
		row := make([]byte, 7) // W [1 4 2]
		if idx, ok := index[num]; ok {
			row[0] = 2
			binary.BigEndian.PutUint32(row[1:5], uint32(stmNum))
			binary.BigEndian.PutUint16(row[5:7], uint16(idx))
		} else if off, ok := offs[num]; ok {
			row[0] = 1
			binary.BigEndian.PutUint32(row[1:5], uint32(off))
		} else {
			row[5], row[6] = 0xff, 0xff // object 0: free
		}
		rows = append(rows, 2) // PNG Up
		for k := range row {
			rows = append(rows, row[k]-prev[k])
		}
		prev = row
	}
	xs := deflate(rows)
	fmt.Fprintf(&b, "%d 0 obj\n<< /Type /XRef /Size %d /W [1 4 2] /Filter /FlateDecode "+
		"/DecodeParms << /Predictor 12 /Columns 7 >> /Length %d %s >>\nstream\n", xrefNum, xrefNum+1, len(xs), trailer)
	b.Write(xs)
	fmt.Fprintf(&b, "\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", xrefOff)
	return b.Bytes()
}

// writeBody writes the header, the objects that are not packed, and an object
// stream numbered stmNum holding the packed ones. It returns each object's
// offset and each packed object's index in the stream.
func (p *testPDF) writeBody(b *bytes.Buffer, stmNum int) (offs, index map[int]int) {
	var head, body bytes.Buffer
	index = map[int]int{}
	for i, o := range p.objs {
		if o.packed {
			fmt.Fprintf(&head, "%d %d ", i+1, body.Len())
			body.WriteString(o.body + "\n")
			index[i+1] = len(index)
		}
	}
	first := head.Len()
	stm := deflate(append(head.Bytes(), body.Bytes()...))
	if p.crypt != nil {
		stm = p.crypt.encrypt(stmNum, stm)
	}
	b.WriteString(header)
	offs = map[int]int{}
	for i, o := range p.objs {
		if !o.packed {
			offs[i+1] = b.Len()
			p.writeObj(b, i+1, o)
		}
	}
	offs[stmNum] = b.Len()
	fmt.Fprintf(b, "%d 0 obj\n<< /Type /ObjStm /N %d /First %d /Filter /FlateDecode /Length %d >>\nstream\n",
		stmNum, len(index), first, len(stm))
	b.Write(stm)
	b.WriteString("\nendstream\nendobj\n")
	return offs, index
}

// hybrid writes a hybrid-reference file: a classic table, in which the packed
// objects are free for readers that know nothing newer, and through /XRefStm a
// cross-reference stream that lists where they really are.
func (p *testPDF) hybrid(trailer string) []byte {
	stmNum := len(p.objs) + 1
	xrefNum := stmNum + 1
	var b bytes.Buffer
	offs, index := p.writeBody(&b, stmNum)

	xrefStm := b.Len()
	offs[xrefNum] = xrefStm
	var rows []byte
	var idx []string
	for num := 1; num <= len(p.objs); num++ {
		if i, ok := index[num]; ok {
			row := make([]byte, 7)
			row[0] = 2
			binary.BigEndian.PutUint32(row[1:5], uint32(stmNum))
			binary.BigEndian.PutUint16(row[5:7], uint16(i))
			rows = append(rows, row...)
			idx = append(idx, fmt.Sprintf("%d 1", num))
		}
	}
	xs := deflate(rows)
	fmt.Fprintf(&b, "%d 0 obj\n<< /Type /XRef /Size %d /W [1 4 2] /Index [%s] /Filter /FlateDecode /Length %d >>\nstream\n",
		xrefNum, xrefNum+1, strings.Join(idx, " "), len(xs))
	b.Write(xs)
	b.WriteString("\nendstream\nendobj\n")

	table := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", xrefNum+1)
	for num := 1; num <= xrefNum; num++ {
		if off, ok := offs[num]; ok {
			fmt.Fprintf(&b, "%010d 00000 n \n", off)
		} else {
			b.WriteString("0000000000 00001 f \n")
		}
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /XRefStm %d %s >>\nstartxref\n%d\n%%%%EOF\n", xrefNum+1, xrefStm, trailer, table)
	return b.Bytes()
}

// update appends an incremental update to base: the given objects, and a classic
// cross-reference section that points back to base's through /Prev.
func update(base []byte, objs map[int]string, size int, trailer string) []byte {
	i := bytes.LastIndex(base, []byte("startxref"))
	var prev int
	fmt.Sscan(string(base[i+len("startxref"):]), &prev)
	b := bytes.NewBuffer(append([]byte(nil), base...))
	var nums []int
	for n := range objs {
		nums = append(nums, n)
	}
	slices.Sort(nums)
	offs := map[int]int{}
	for _, n := range nums {
		offs[n] = b.Len()
		fmt.Fprintf(b, "%d 0 obj\n%s\nendobj\n", n, objs[n])
	}
	xref := b.Len()
	b.WriteString("xref\n0 1\n0000000000 65535 f \n")
	for _, n := range nums {
		fmt.Fprintf(b, "%d 1\n%010d 00000 n \n", n, offs[n])
	}
	fmt.Fprintf(b, "trailer\n<< /Size %d /Prev %d %s >>\nstartxref\n%d\n%%%%EOF\n", size, prev, trailer, xref)
	return b.Bytes()
}

func deflate(b []byte) []byte {
	var out bytes.Buffer
	w := zlib.NewWriter(&out)
	w.Write(b)
	w.Close()
	return out.Bytes()
}

// utf16Hex is a text string in UTF-16BE with a byte order mark, as a hex string.
func utf16Hex(s string) string {
	var b strings.Builder
	b.WriteString("<FEFF")
	for _, u := range utf16.Encode([]rune(s)) {
		fmt.Fprintf(&b, "%04X", u)
	}
	b.WriteString(">")
	return b.String()
}

// onePage adds a catalog, a page tree with one page showing content, and font
// F1, and returns the catalog's number. font is the font's dictionary.
func (p *testPDF) onePage(content []byte, contentDict, font string) int {
	fontNum := p.add(font)
	contentNum := p.addStream(contentDict, content)
	pagesNum := p.next() + 1
	pageNum := p.add(fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 612 792] "+
		"/Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", pagesNum, fontNum, contentNum))
	p.add(fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", pageNum))
	return p.add(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesNum))
}

const helvetica = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>"

// testCrypt encrypts a test file with the standard security handler, for the
// empty user password.
type testCrypt struct {
	key  []byte
	aes  bool
	id   []byte
	dict string // the /Encrypt dictionary
}

// newRC4 sets up RC4 encryption: revision 2 with a 40-bit key, or revision 3 with
// a 128-bit one.
func newRC4(r int) *testCrypt {
	id := []byte("0123456789abcdef")
	n := 5
	if r == 3 {
		n = 16
	}
	o := ownerValue([]byte("owner"), r, n)
	p := int32(-3904)
	key, u := userValues(o, p, id, r, n, true)
	v := map[int]string{2: "/V 1 /R 2", 3: "/V 2 /R 3 /Length 128"}[r]
	return &testCrypt{key: key, id: id, dict: fmt.Sprintf("<< /Filter /Standard %s /O <%x> /U <%x> /P %d >>", v, o, u, p)}
}

// newAESV2 sets up AES-128 encryption: version 4, revision 4, with a /StdCF crypt
// filter whose method is AESV2.
func newAESV2() *testCrypt {
	id := []byte("fedcba9876543210")
	o := ownerValue([]byte("owner"), 4, 16)
	p := int32(-1028)
	key, u := userValues(o, p, id, 4, 16, true)
	return &testCrypt{key: key, aes: true, id: id, dict: fmt.Sprintf("<< /Filter /Standard /V 4 /R 4 /Length 128 "+
		"/CF << /StdCF << /CFM /AESV2 /AuthEvent /DocOpen /Length 16 >> >> /StmF /StdCF /StrF /StdCF "+
		"/O <%x> /U <%x> /P %d >>", o, u, p)}
}

// newAES256 sets up AES-256 encryption: version 5, revision 6.
func newAES256() *testCrypt {
	key := []byte("0123456789ABCDEF0123456789abcdef")
	vsalt, ksalt := []byte("vsaltval"), []byte("ksaltval")
	u := append(append(testHash2B(nil, vsalt, nil), vsalt...), ksalt...)
	ue := aesNoPad(testHash2B(nil, ksalt, nil), key)
	o := bytes.Repeat([]byte{0x5A}, 48) // the owner password is not tested
	oe := bytes.Repeat([]byte{0xA5}, 32)
	p := int32(-1028)
	perm := make([]byte, 16)
	binary.LittleEndian.PutUint32(perm, uint32(p))
	copy(perm[4:], "\xff\xff\xff\xffTadb0000")
	block, _ := aes.NewCipher(key)
	block.Encrypt(perm, perm)
	return &testCrypt{key: key, aes: true, id: []byte("aes256-file-id-0"), dict: fmt.Sprintf(
		"<< /Filter /Standard /V 5 /R 6 /Length 256 /CF << /StdCF << /CFM /AESV3 /AuthEvent /DocOpen /Length 32 >> >> "+
			"/StmF /StdCF /StrF /StdCF /O <%x> /U <%x> /OE <%x> /UE <%x> /Perms <%x> /P %d >>", o, u, oe, ue, perm, p)}
}

// testHash2B is algorithm 2.B in full, with a password and the user key, written
// apart from the reader's hash2B so the two check each other.
func testHash2B(pw, salt, udata []byte) []byte {
	h := sha256.Sum256(slices.Concat(pw, salt, udata))
	k := h[:]
	var e []byte
	for round := 0; round < 64 || int(e[len(e)-1]) > round-32; round++ {
		seq := slices.Concat(pw, k, udata)
		k1 := make([]byte, 0, 64*len(seq))
		for range 64 {
			k1 = append(k1, seq...)
		}
		block, _ := aes.NewCipher(k[:16])
		e = make([]byte, len(k1))
		cipher.NewCBCEncrypter(block, k[16:32]).CryptBlocks(e, k1)
		var n big.Int
		switch n.SetBytes(e[:16]).Mod(&n, big.NewInt(3)).Int64() {
		case 0:
			s := sha256.Sum256(e)
			k = s[:]
		case 1:
			s := sha512.Sum384(e)
			k = s[:]
		case 2:
			s := sha512.Sum512(e)
			k = s[:]
		}
	}
	return k[:32]
}

func aesNoPad(key, data []byte) []byte {
	block, _ := aes.NewCipher(key)
	out := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, make([]byte, 16)).CryptBlocks(out, data)
	return out
}

// ownerValue computes /O from an owner password, for the empty user password
// (algorithm 3).
func ownerValue(owner []byte, r, n int) []byte {
	sum := md5.Sum(append(append([]byte(nil), owner...), padding[:32-len(owner)]...))
	k := sum[:]
	if r >= 3 {
		for range 50 {
			s := md5.Sum(k)
			k = s[:]
		}
	}
	k = k[:n]
	o := rc4XOR(k, padding)
	if r >= 3 {
		x := make([]byte, n)
		for i := 1; i <= 19; i++ {
			for j := range x {
				x[j] = k[j] ^ byte(i)
			}
			o = rc4XOR(x, o)
		}
	}
	return o
}

// userValues computes the file key and /U for the empty user password
// (algorithms 2, 4 and 5), independently of md5Key.
func userValues(o []byte, p int32, id []byte, r, n int, meta bool) (key, u []byte) {
	h := md5.New()
	h.Write(padding)
	h.Write(o)
	binary.Write(h, binary.LittleEndian, p)
	h.Write(id)
	if r >= 4 && !meta {
		h.Write([]byte{0xff, 0xff, 0xff, 0xff})
	}
	key = h.Sum(nil)
	if r >= 3 {
		for range 50 {
			s := md5.Sum(key[:n])
			key = s[:]
		}
	}
	key = key[:n]
	if r == 2 {
		return key, rc4XOR(key, padding)
	}
	s := md5.Sum(append(append([]byte(nil), padding...), id...))
	x := rc4XOR(key, s[:])
	k := make([]byte, n)
	for i := 1; i <= 19; i++ {
		for j := range k {
			k[j] = key[j] ^ byte(i)
		}
		x = rc4XOR(k, x)
	}
	return key, append(x, bytes.Repeat([]byte{0x11}, 16)...) // the last 16 bytes are arbitrary
}

func (c *testCrypt) objectKey(num int) []byte {
	if len(c.key) == 32 {
		return c.key
	}
	b := append(append([]byte(nil), c.key...), byte(num), byte(num>>8), byte(num>>16), 0, 0)
	if c.aes {
		b = append(b, "sAlT"...)
	}
	s := md5.Sum(b)
	return s[:min(len(c.key)+5, 16)]
}

func (c *testCrypt) encrypt(num int, data []byte) []byte {
	key := c.objectKey(num)
	if !c.aes {
		return rc4XOR(key, data)
	}
	n := 16 - len(data)%16
	plain := append(append([]byte(nil), data...), bytes.Repeat([]byte{byte(n)}, n)...)
	iv := []byte("initialvector-16")
	block, _ := aes.NewCipher(key)
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, plain)
	return append(append([]byte(nil), iv...), out...)
}

// str returns s encrypted for object num, as a hex string.
func (c *testCrypt) str(num int, s string) string {
	return "<" + hex.EncodeToString(c.encrypt(num, []byte(s))) + ">"
}

// trailer returns the trailer entries an encrypted file needs.
func (c *testCrypt) trailer(encNum int) string {
	return fmt.Sprintf("/Encrypt %d 0 R /ID [<%x> <%x>]", encNum, c.id, c.id)
}
