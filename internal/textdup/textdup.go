// Package textdup finds documents that are mostly the same text: revisions of one
// manual, a page saved twice by a scraper with different navigation around it, the
// same article as HTML and as text.
//
// Comparing bytes misses these, and comparing every pair of texts word by word
// costs too much once there are thousands. So each document is summarized once,
// and the summaries are compared:
//
//  1. Words. Text is lowercased and split into runs of letters and digits; every
//     other rune separates words. Markup leftovers and punctuation drop out.
//  2. Shingles. Every run of five consecutive words is hashed to 64 bits. Runs keep
//     word order: two articles on one topic share most of their words, but few
//     runs of five.
//  3. A signature. Each of 128 hash functions keeps its least value over the
//     document's shingles. Among all the shingles of two documents, the one a
//     function ranks lowest is equally likely to be any of them, and the two
//     agree on that function exactly when it is in both. So the share of
//     functions they agree on estimates their Jaccard similarity: the shingles
//     they share, over the shingles either has.
//  4. Bands. Find cuts each signature into 21 bands of 6 values and compares only
//     documents that agree on a whole band. Near-duplicates almost always do;
//     different documents almost never do.
//
// Similarity counts shared text wherever it is, which leaves two things to the
// caller. Tags are words too: an article as raw HTML scores about 0.75 against
// its own text, so strip tags before signing a web page. And navigation counts
// like any other text: two pages whose shared header and footer make up nine
// tenths of each score about 0.8, whatever their articles say.
package textdup

import (
	"math"
	"unicode"
	"unicode/utf8"
)

// MinWords is the least text worth comparing. Below it, two documents share
// shingles by accident.
const MinWords = 50

const (
	// shingleWords is how many consecutive words make a shingle.
	shingleWords = 5

	// numHashes is how many hash functions a signature records. Each one adds a
	// little to the cost of signing and sharpens the estimate: at 128, its
	// standard error is √(s(1−s)/128), 0.035 at a similarity of 0.8.
	numHashes = 128
)

// Signature summarizes a document's text for comparison: MinHash over word
// shingles. It is a fixed array, so fifty thousand of them are one allocation.
type Signature struct {
	mins [numHashes]uint64 // per hash function, the least hash of any shingle
}

// Sign summarizes text. ok is false when the text is too short to compare.
func Sign(text string) (sig Signature, ok bool) {
	for i := range sig.mins {
		sig.mins[i] = math.MaxUint64
	}
	s := shingles{words: words{text: text}}
	// Shingles are folded in batches, four hash functions at a time: their seeds
	// and running minimums stay in registers across the batch, and the four
	// mixes run side by side. It signs 1.5 times as fast as a shingle at a time.
	var batch [256]uint64
	n := 0
	for {
		h, more := s.next()
		if !more {
			break
		}
		batch[n] = h
		n++
		if n == len(batch) {
			sig.fold(batch[:])
			n = 0
		}
	}
	if s.count < MinWords {
		return Signature{}, false
	}
	sig.fold(batch[:n])
	return sig, true
}

// fold lowers each minimum to the least value its hash function takes over hashes.
func (s *Signature) fold(hashes []uint64) {
	for i := 0; i < numHashes; i += 4 {
		s0, s1, s2, s3 := seeds[i], seeds[i+1], seeds[i+2], seeds[i+3]
		m0, m1, m2, m3 := s.mins[i], s.mins[i+1], s.mins[i+2], s.mins[i+3]
		for _, h := range hashes {
			m0 = min(m0, mix64(h^s0))
			m1 = min(m1, mix64(h^s1))
			m2 = min(m2, mix64(h^s2))
			m3 = min(m3, mix64(h^s3))
		}
		s.mins[i], s.mins[i+1], s.mins[i+2], s.mins[i+3] = m0, m1, m2, m3
	}
}

// Similarity estimates the Jaccard similarity of two documents' shingle sets.
// The zero Signature, which Sign returns for text too short to compare, is
// similar to nothing.
func Similarity(a, b Signature) float64 {
	if a.zero() || b.zero() {
		return 0
	}
	return similarity(&a, &b)
}

// similarity is the share of hash functions on which two signatures agree.
func similarity(a, b *Signature) float64 {
	n := 0
	for i := range a.mins {
		if a.mins[i] == b.mins[i] {
			n++
		}
	}
	return float64(n) / numHashes
}

// zero reports whether s is the zero Signature. A real signature is never all
// zeros: each of its 128 values would need a shingle that hashes to exactly zero.
func (s *Signature) zero() bool { return s.mins == [numHashes]uint64{} }

// seeds tell the hash functions apart: function i hashes a shingle h as
// mix64(h ^ seeds[i]). They are the start of splitmix64's sequence, fixed, so a
// signature means the same on every run and every machine.
var seeds = func() (s [numHashes]uint64) {
	var state uint64
	for i := range s {
		state += 0x9e3779b97f4a7c15
		s[i] = mix64(state)
	}
	return s
}()

// mix64 is splitmix64's finalizer. Each input bit flips each output bit with
// probability near one half, so related inputs, such as one shingle under two
// seeds, give unrelated outputs. It is a bijection: distinct inputs stay distinct.
func mix64(z uint64) uint64 {
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// FNV-1a, 64-bit.
const (
	fnvOffset = 14695981039346656037
	fnvPrime  = 1099511628211
)

// shingles walks a text's shingles: the hashes of every run of five consecutive
// words.
type shingles struct {
	words
	ring  [shingleWords]uint64 // the last five word hashes; the oldest at count % 5
	count int                  // words read so far
}

// next returns the next shingle's hash, or false at the end of the text.
//
// A shingle's hash is FNV-1a over its five word hashes, then mix64. FNV-1a alone
// mixes poorly: its low bits depend only on the low bits of its input. mix64
// spreads every input bit over the whole hash, which MinHash needs.
func (s *shingles) next() (uint64, bool) {
	for s.words.next() {
		s.ring[s.count%shingleWords] = s.hash
		s.count++
		if s.count < shingleWords {
			continue
		}
		h := uint64(fnvOffset)
		for k := range shingleWords {
			h = (h ^ s.ring[(s.count+k)%shingleWords]) * fnvPrime
		}
		return mix64(h), true
	}
	return 0, false
}

// words walks a text's words, hashing each as it reads it.
//
// A word is a run of letters and digits, lowercased. Marks join the run too: the
// vowel signs of Hindi or Thai are marks, and so is the accent of a decomposed é;
// splitting at them would break most words. Chinese and Japanese are written
// without spaces, so there each ideograph or kana is a word on its own, with any
// marks that follow it. A whole sentence would otherwise be one word.
type words struct {
	text string
	pos  int    // byte offset of the next rune to read
	hash uint64 // FNV-1a of the word just read, lowercased, as UTF-8
}

// next reads the next word and reports whether there was one.
func (w *words) next() bool {
	h := uint64(fnvOffset)
	in := false    // a word has started
	alone := false // the word is an ideograph, and takes no letters after it
	for w.pos < len(w.text) {
		if c := w.text[w.pos]; c < utf8.RuneSelf {
			l := asciiWord[c]
			switch {
			case l == 0: // a separator
				w.pos++
				if in {
					w.hash = h
					return true
				}
			case alone: // the letter starts the next word
				w.hash = h
				return true
			default:
				h = (h ^ uint64(l)) * fnvPrime
				in = true
				w.pos++
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(w.text[w.pos:])
		switch {
		case ideograph(r):
			if in {
				w.hash = h
				return true
			}
			alone = true
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if alone {
				w.hash = h
				return true
			}
		case unicode.IsMark(r):
			if !in { // a stray mark belongs to no word
				w.pos += size
				continue
			}
		default: // a separator, invalid UTF-8 among them
			w.pos += size
			if in {
				w.hash = h
				return true
			}
			continue
		}
		var buf [utf8.UTFMax]byte
		for _, b := range buf[:utf8.EncodeRune(buf[:], unicode.ToLower(r))] {
			h = (h ^ uint64(b)) * fnvPrime
		}
		in = true
		w.pos += size
	}
	w.hash = h
	return in
}

// asciiWord maps an ASCII byte that can be part of a word to its lowercase, and
// a separator to 0. Most text is mostly ASCII; the table keeps it off the Unicode
// tables.
var asciiWord = func() (t [utf8.RuneSelf]byte) {
	for c := byte('0'); c <= '9'; c++ {
		t[c] = c
	}
	for c := byte('a'); c <= 'z'; c++ {
		t[c], t[c-'a'+'A'] = c, c
	}
	return t
}()

// ideograph reports whether r is a Chinese character or Japanese kana. All of
// them lie at U+2E80 and above, which spares every other script the table lookups.
func ideograph(r rune) bool {
	return r >= 0x2e80 && unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana)
}
