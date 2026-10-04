package pdf

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rc4"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"errors"
)

type cryptMethod uint8

const (
	cryptNone cryptMethod = iota
	cryptRC4
	cryptAES // AES-CBC with the IV in front: AESV2, and AESV3 with a 32-byte key
)

// security decrypts a document under the standard security handler when the
// empty user password opens it. That is the common case: such a file opens in
// any viewer without a prompt, and the encryption only backs its restrictions
// on printing or copying.
type security struct {
	key    []byte
	str    cryptMethod // for strings
	stm    cryptMethod // for streams
	cf     map[name]cryptMethod
	encNum uint32 // the encryption dictionary's own object, which is never encrypted
	meta   bool   // metadata streams are encrypted too
}

var errNeedsPassword = errors.New("pdf: the document needs a password")

// padding is the fixed string a password is padded with; the empty password is
// all padding.
var padding = []byte("\x28\xbf\x4e\x5e\x4e\x75\x8a\x41\x64\x00\x4e\x56\xff\xfa\x01\x08" +
	"\x2e\x2e\x00\xb6\xd0\x68\x3e\x80\x2f\x0c\xa9\xfe\x64\x53\x69\x7a")

// newSecurity sets up decryption from the encryption dictionary, or fails when
// the scheme is not one supported or the empty password does not open the file.
func newSecurity(enc dict, id0 []byte) (*security, error) {
	if f, _ := enc["Filter"].(name); f != "Standard" {
		return nil, errors.New("pdf: unsupported security handler")
	}
	v, _ := enc["V"].(int64)
	r, _ := enc["R"].(int64)
	o, _ := enc["O"].(string)
	u, _ := enc["U"].(string)
	p, _ := enc["P"].(int64)
	s := &security{meta: true}
	if b, ok := enc["EncryptMetadata"].(bool); ok {
		s.meta = b
	}
	switch {
	case (v == 1 || v == 2) && (r == 2 || r == 3):
		n := 5
		if v == 2 {
			if l, ok := enc["Length"].(int64); ok { // in bits; 40 when absent
				n = int(l / 8)
				if l >= 5 && l <= 16 { // a few writers give it in bytes
					n = int(l)
				}
			}
			if n < 5 || n > 16 {
				return nil, errors.New("pdf: bad key length")
			}
		}
		key, ok := md5Key(o, u, uint32(p), id0, int(r), n, true)
		if !ok {
			return nil, errNeedsPassword
		}
		s.key, s.str, s.stm = key, cryptRC4, cryptRC4
	case v == 4 && r == 4:
		s.readFilters(enc)
		key, ok := md5Key(o, u, uint32(p), id0, 4, 16, s.meta)
		if !ok {
			return nil, errNeedsPassword
		}
		s.key = key
	case v == 5 && (r == 5 || r == 6):
		s.readFilters(enc)
		ue, _ := enc["UE"].(string)
		key, ok := aes256Key(u, ue, int(r))
		if !ok {
			return nil, errNeedsPassword
		}
		s.key = key
	default:
		return nil, errors.New("pdf: unsupported encryption")
	}
	return s, nil
}

// readFilters reads the crypt filters of a version 4 or 5 dictionary, and which
// of them strings and streams use. Both default to Identity: no encryption.
func (s *security) readFilters(enc dict) {
	s.cf = map[name]cryptMethod{"Identity": cryptNone}
	cfs, _ := enc["CF"].(dict)
	for n, v := range cfs {
		c, _ := v.(dict)
		switch m, _ := c["CFM"].(name); m {
		case "V2":
			s.cf[n] = cryptRC4
		case "AESV2", "AESV3":
			s.cf[n] = cryptAES
		default:
			s.cf[n] = cryptNone
		}
	}
	method := func(k name) cryptMethod {
		n, _ := enc[k].(name)
		return s.cf[n] // an unknown name reads as Identity
	}
	s.str, s.stm = method("StrF"), method("StmF")
}

// md5Key computes the file key from the empty password (algorithm 2 of the PDF
// specification) and checks it against /U (algorithms 4 and 5). A mismatch means
// the file has a real user password.
func md5Key(o, u string, p uint32, id0 []byte, r, n int, meta bool) ([]byte, bool) {
	if len(o) < 32 || len(u) < 32 {
		return nil, false
	}
	h := md5.New()
	h.Write(padding)
	h.Write([]byte(o[:32]))
	var pb [4]byte
	binary.LittleEndian.PutUint32(pb[:], p)
	h.Write(pb[:])
	h.Write(id0)
	if r >= 4 && !meta {
		h.Write([]byte{0xff, 0xff, 0xff, 0xff})
	}
	key := h.Sum(nil)
	if r >= 3 {
		for range 50 {
			sum := md5.Sum(key[:n])
			key = sum[:]
		}
	}
	key = key[:n]

	if r == 2 {
		return key, bytes.Equal(rc4XOR(key, padding), []byte(u[:32]))
	}
	h.Reset()
	h.Write(padding)
	h.Write(id0)
	x := rc4XOR(key, h.Sum(nil))
	k := make([]byte, n)
	for i := 1; i <= 19; i++ {
		for j := range k {
			k[j] = key[j] ^ byte(i)
		}
		x = rc4XOR(k, x)
	}
	return key, bytes.Equal(x, []byte(u[:16]))
}

// aes256Key checks the empty password against /U and unwraps the file key from
// /UE (algorithms 2.A and 2.B; revision 5 is the hash without its rounds).
func aes256Key(u, ue string, r int) ([]byte, bool) {
	if len(u) < 48 || len(ue) < 32 {
		return nil, false
	}
	if !bytes.Equal(hash2B([]byte(u[32:40]), r), []byte(u[:32])) {
		return nil, false
	}
	block, err := aes.NewCipher(hash2B([]byte(u[40:48]), r))
	if err != nil {
		return nil, false
	}
	key := make([]byte, 32)
	cipher.NewCBCDecrypter(block, make([]byte, 16)).CryptBlocks(key, []byte(ue[:32]))
	return key, true
}

// hash2B is the revision 6 password hash of the empty user password with salt.
// Each round hashes with SHA-256, -384 or -512, chosen by the round's own output.
func hash2B(salt []byte, r int) []byte {
	sum := sha256.Sum256(salt)
	k := sum[:]
	if r == 5 {
		return k
	}
	var e []byte
	for i := 0; i < 64 || int(e[len(e)-1]) > i-32; i++ {
		k1 := bytes.Repeat(k, 64) // password, key and user data; here the key alone
		block, _ := aes.NewCipher(k[:16])
		e = make([]byte, len(k1))
		cipher.NewCBCEncrypter(block, k[16:32]).CryptBlocks(e, k1)
		mod := 0
		for _, b := range e[:16] {
			mod += int(b) // 256 ≡ 1 (mod 3), so the bytes' sum has the number's residue
		}
		switch mod % 3 {
		case 0:
			s := sha256.Sum256(e)
			k = s[:]
		case 1:
			s := sha512.Sum384(e)
			k = s[:]
		default:
			s := sha512.Sum512(e)
			k = s[:]
		}
	}
	return k[:32]
}

func rc4XOR(key, data []byte) []byte {
	c, err := rc4.NewCipher(key)
	if err != nil {
		return nil
	}
	out := make([]byte, len(data))
	c.XORKeyStream(out, data)
	return out
}

// objectKey derives the key for one object (algorithm 1). AES-256 uses the file
// key as it is.
func (s *security) objectKey(r ref, aesSalt bool) []byte {
	if len(s.key) == 32 {
		return s.key
	}
	b := append([]byte(nil), s.key...)
	b = append(b, byte(r.num), byte(r.num>>8), byte(r.num>>16), byte(r.gen), byte(r.gen>>8))
	if aesSalt {
		b = append(b, "sAlT"...)
	}
	sum := md5.Sum(b)
	return sum[:min(len(s.key)+5, 16)]
}

// decrypt decrypts the data of object r with method m.
func (s *security) decrypt(m cryptMethod, r ref, data []byte) []byte {
	switch m {
	case cryptRC4:
		return rc4XOR(s.objectKey(r, false), data)
	case cryptAES:
		if len(data) < 16 {
			return nil
		}
		iv, body := data[:16], data[16:]
		body = body[:len(body)-len(body)%16]
		block, err := aes.NewCipher(s.objectKey(r, true))
		if err != nil || len(body) == 0 {
			return nil
		}
		out := make([]byte, len(body))
		cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, body)
		if n := int(out[len(out)-1]); n >= 1 && n <= 16 && n <= len(out) {
			if bytes.Count(out[len(out)-n:], []byte{byte(n)}) == n {
				out = out[:len(out)-n] // PKCS#7 padding
			}
		}
		return out
	}
	return data
}

// streamMethod picks how a stream is encrypted. Cross-reference streams never
// are; metadata is when the dictionary says so; a stream may name its own crypt
// filter.
func (s *security) streamMethod(st *stream, filters []name, parms []dict) cryptMethod {
	switch t, _ := st.hdr["Type"].(name); {
	case t == "XRef":
		return cryptNone
	case t == "Metadata" && !s.meta:
		return cryptNone
	}
	if len(filters) > 0 && filters[0] == "Crypt" {
		n, _ := parms[0]["Name"].(name)
		if m, ok := s.cf[n]; ok {
			return m
		}
		return cryptNone // no name means Identity
	}
	return s.stm
}

// decryptStrings decrypts every string inside v, an object read from the file.
// Strings inside object streams are not encrypted on their own: the stream is.
func (s *security) decryptStrings(v any, r ref) any {
	switch t := v.(type) {
	case string:
		return string(s.decrypt(s.str, r, []byte(t)))
	case array:
		for i := range t {
			t[i] = s.decryptStrings(t[i], r)
		}
	case dict:
		for k, x := range t {
			t[k] = s.decryptStrings(x, r)
		}
	case *stream:
		s.decryptStrings(t.hdr, r)
	}
	return v
}
