package textdup

import (
	"cmp"
	"math/bits"
	"slices"
)

// Match is a pair of documents, by index into the slice given to Find, and their
// estimated similarity.
type Match struct {
	A, B  int
	Score float64
}

// bands and rows cut a signature for Find. Two documents become candidates when
// they agree on all 6 values of at least one of 21 bands. Documents of similarity
// s agree on each value with probability s, so they become candidates with
// probability 1 − (1 − s⁶)²¹:
//
//	s   0.2    0.3    0.4    0.5    0.6    0.7    0.75   0.8     0.9
//	P   0.001  0.015  0.083  0.28   0.63   0.93   0.984  0.9983  0.9999999
//
// Six rows is the most that still catches 99% at 0.8: seven rows, in 18 bands,
// catch 98.6%. Fewer rows compare more pairs that do not match: at 0.4, five rows
// in 25 bands make 23% of pairs candidates, against 8% here. A candidate that does
// not match costs one comparison, not a wrong answer. 21 × 6 uses 126 of the 128
// values.
//
// Find keeps a pair by its estimate, the share of values that agree, and a known
// count of agreements makes a miss rarer still. Twenty disagreements cannot touch
// all 21 bands, so a pair that agrees on 108 of 128 values, an estimate of 0.84,
// is always a candidate. At 103, the least that reaches 0.8, one pair in 30,000
// is missed.
const (
	bands = 21
	rows  = 6
)

// Find returns every pair whose estimated similarity is at least threshold,
// using locality-sensitive hashing so it does not compare every pair. Pairs are
// ordered by A, then B.
//
// The bands are tuned for thresholds of 0.8 and above. Below that, Find misses
// pairs: 0.5% of those estimated at 0.75, 4% at 0.7, 36% at 0.6. Zero signatures,
// from text too short to sign, match nothing.
//
// Every pair is listed, so a thousand copies of one text are half a million
// matches. Groups folds them back into one set.
func Find(sigs []Signature, threshold float64) []Match {
	var live []int
	for i := range sigs {
		if !sigs[i].zero() {
			live = append(live, i)
		}
	}
	if len(live) < 2 {
		return nil
	}

	// Bucket the signatures band by band. Each signature's band key and index are
	// packed into one word, index in the low bits, and sorted: equal keys then sit
	// together, in index order. Keys keep 48 bits at 50,000 signatures, so two
	// bands that differ share a key once in 2⁴⁸ pairs, which wastes one comparison.
	idxBits := bits.Len(uint(len(sigs) - 1))
	keyMask := ^uint64(0) << idxBits
	packed := make([]uint64, len(live))
	var members []int // the buckets' members, one bucket after another
	starts := []int{0}
	for b := range bands {
		for k, i := range live {
			packed[k] = bandKey(&sigs[i], b)&keyMask | uint64(i)
		}
		slices.Sort(packed)
		for lo := 0; lo < len(packed); {
			hi := lo + 1
			for hi < len(packed) && packed[hi]&keyMask == packed[lo]&keyMask {
				hi++
			}
			if hi-lo > 1 {
				for _, p := range packed[lo:hi] {
					members = append(members, int(p&^keyMask))
				}
				starts = append(starts, len(members))
			}
			lo = hi
		}
	}

	// Index the buckets by member. Document i is in buckets in[at[i]:at[i+1]].
	at := make([]int, len(sigs)+1)
	for _, i := range members {
		at[i+1]++
	}
	for i := range sigs {
		at[i+1] += at[i]
	}
	in := make([]int, len(members))
	fill := slices.Clone(at[:len(sigs)])
	for k := range len(starts) - 1 {
		for _, i := range members[starts[k]:starts[k+1]] {
			in[fill[i]] = k
			fill[i]++
		}
	}

	// Score each candidate pair once, document by document. A pair that shares
	// several bands is met several times; seen[j] == i+1 marks (i, j) as scored.
	seen := make([]int, len(sigs))
	var out []Match
	for _, i := range live {
		row := len(out)
		for _, k := range in[at[i]:at[i+1]] {
			bucket := members[starts[k]:starts[k+1]]
			pos, _ := slices.BinarySearch(bucket, i)
			for _, j := range bucket[pos+1:] {
				if seen[j] == i+1 {
					continue
				}
				seen[j] = i + 1
				if s := similarity(&sigs[i], &sigs[j]); s >= threshold {
					out = append(out, Match{A: i, B: j, Score: s})
				}
			}
		}
		slices.SortFunc(out[row:], func(x, y Match) int { return cmp.Compare(x.B, y.B) })
	}
	return out
}

// bandKey hashes band b of a signature. Its values are minimums, so their top
// bits are usually zero; mixing spreads them over the whole key, whose top bits
// are the ones Find keeps.
func bandKey(s *Signature, b int) uint64 {
	var h uint64
	for _, m := range s.mins[b*rows : (b+1)*rows] {
		h = mix64(h ^ m)
	}
	return h
}

// Groups joins matches into sets of documents that are near-duplicates of one
// another, each sorted, the sets ordered by their first member. Documents in no
// match are left out.
//
// Joining is transitive. If A matches B and B matches C, the three are one set,
// even when A and C do not match: revisions that drift a little at a time chain
// into one set, though the first and the last may share little. A caller that
// keeps one document of a set and drops the rest should compare each against the
// one it keeps.
func Groups(matches []Match) [][]int {
	// Union-find. Every root is the least index in its set, so it is also the
	// set's first member.
	parent := map[int]int{}
	var docs []int
	root := func(x int) int {
		p, ok := parent[x]
		if !ok {
			parent[x] = x
			docs = append(docs, x)
			return x
		}
		for p != x {
			gp := parent[p]
			parent[x] = gp // path halving keeps chains short
			x, p = gp, parent[gp]
		}
		return x
	}
	for _, m := range matches {
		a, b := root(m.A), root(m.B)
		if a > b {
			a, b = b, a
		}
		parent[b] = a
	}

	sets := map[int][]int{}
	for _, d := range docs {
		r := root(d)
		sets[r] = append(sets[r], d)
	}
	out := make([][]int, 0, len(sets))
	for _, s := range sets {
		slices.Sort(s)
		out = append(out, s)
	}
	slices.SortFunc(out, func(x, y []int) int { return cmp.Compare(x[0], y[0]) })
	return out
}
