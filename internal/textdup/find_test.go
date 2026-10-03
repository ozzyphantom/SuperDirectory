package textdup

import (
	"math"
	"math/rand"
	"runtime"
	"slices"
	"sync"
	"testing"
)

// library is a folder's worth of documents with near-duplicates planted in it.
type library struct {
	texts   []string
	family  []int    // documents of one family come from one text
	planted [][2]int // pairs of drafts of one text, lower index first
	decoys  [][2]int // pairs that share some paragraphs and are not near-duplicates
}

// newLibrary builds n documents of the given number of paragraphs:
//
//   - n/8 pairs, a text and a revision of it;
//   - n/20 triples, three drafts, each revising the last;
//   - n/20 decoy pairs, which share 40% to 80% of their paragraphs;
//   - unrelated documents for the rest, a third of them pages of one of four
//     sites, carrying its navigation.
//
// The order is shuffled, so related documents do not sit together.
func newLibrary(n, paragraphs int, seed int64) library {
	r := rand.New(rand.NewSource(seed))
	type draft struct {
		text   string
		family int
	}
	var drafts []draft
	fresh := func() (*writer, doc) {
		w := newWriter(r.Int63(), r.Intn(len(subjects)))
		return w, w.document(paragraphs)
	}
	pairs, triples, decoys := n/8, n/20, n/20
	f := 0
	for ; f < pairs; f++ {
		w, d := fresh()
		drafts = append(drafts, draft{d.text(), f}, draft{revise(w, d, f), f})
	}
	for ; f < pairs+triples; f++ {
		w, d := fresh()
		second := w.edited(d)
		drafts = append(drafts, draft{d.text(), f}, draft{second.text(), f}, draft{w.edited(second).text(), f})
	}
	for ; f < pairs+triples+decoys; f++ {
		w, d := fresh()
		other := slices.Clone(d)
		keep := paragraphs*2/5 + r.Intn(paragraphs*2/5+1)
		for _, i := range r.Perm(paragraphs)[keep:] {
			other[i] = w.paragraph()
		}
		drafts = append(drafts, draft{d.text(), f}, draft{other.text(), f})
	}
	for ; len(drafts) < n; f++ {
		_, d := fresh()
		text := d.text()
		if site := r.Intn(12); site < 4 {
			header, footer := chrome(int64(site), wordCount(text)/10)
			text = wrapped(text, header, footer)
		}
		drafts = append(drafts, draft{text, f})
	}
	r.Shuffle(len(drafts), func(i, j int) { drafts[i], drafts[j] = drafts[j], drafts[i] })

	lib := library{texts: make([]string, n), family: make([]int, n)}
	members := make([][]int, f)
	for i, d := range drafts {
		lib.texts[i], lib.family[i] = d.text, d.family
		members[d.family] = append(members[d.family], i)
	}
	for fam, m := range members {
		for x, a := range m {
			for _, b := range m[x+1:] {
				if fam < pairs+triples {
					lib.planted = append(lib.planted, [2]int{a, b})
				} else {
					lib.decoys = append(lib.decoys, [2]int{a, b})
				}
			}
		}
	}
	return lib
}

// revise makes a near-duplicate of d by one of the edits the calibration measured.
func revise(w *writer, d doc, kind int) string {
	switch kind % 5 {
	case 0:
		return w.edited(d).text()
	case 1:
		return w.rewritten(d).text()
	case 2:
		text := d.text()
		header, footer := chrome(w.r.Int63(), wordCount(text)/10)
		return wrapped(text, header, footer)
	case 3:
		return w.reordered(d).text()
	default:
		return stripped(asHTML(w.title(), d, w.s.slug))
	}
}

// signAll signs texts on every core.
func signAll(tb testing.TB, texts []string) []Signature {
	sigs := make([]Signature, len(texts))
	workers := runtime.GOMAXPROCS(0)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := w; i < len(texts); i += workers {
				var ok bool
				if sigs[i], ok = Sign(texts[i]); !ok {
					tb.Errorf("document %d not signed", i)
				}
			}
		})
	}
	wg.Wait()
	return sigs
}

// TestFindRecoversPlantedPairs runs Find on 2,000 documents of twenty paragraphs:
// 250 pairs and 100 triples of drafts, 550 planted pairs in all, at true
// similarity 0.874 to 0.982, median 0.955; 100 decoy pairs sharing 40% to 80% of
// their paragraphs, at 0.175 to 0.714; and 1,000 unrelated documents. Measured:
// Find returns the 550 planted pairs and nothing else, and comparing every pair
// finds the same 550, so the bands lose none.
func TestFindRecoversPlantedPairs(t *testing.T) {
	lib := newLibrary(2000, 20, 1)
	sigs := signAll(t, lib.texts)
	got := Find(sigs, 0.8)

	found := map[[2]int]bool{}
	for k, m := range got {
		if m.A >= m.B {
			t.Fatalf("match %v: want A < B", m)
		}
		if k > 0 && (got[k-1].A > m.A || got[k-1].A == m.A && got[k-1].B >= m.B) {
			t.Fatalf("match %v after %v: want pairs ordered by A, then B", m, got[k-1])
		}
		if s := Similarity(sigs[m.A], sigs[m.B]); m.Score != s || s < 0.8 {
			t.Errorf("match %v: Similarity is %.3f", m, s)
		}
		found[[2]int{m.A, m.B}] = true
	}

	sets := map[int]map[uint64]bool{}
	set := func(i int) map[uint64]bool {
		if sets[i] == nil {
			sets[i] = shingleSet(lib.texts[i])
		}
		return sets[i]
	}
	var missed int
	var planted []float64
	for _, p := range lib.planted {
		planted = append(planted, jaccard(set(p[0]), set(p[1])))
		if !found[p] {
			missed++
		}
	}
	recall := 1 - float64(missed)/float64(len(lib.planted))
	if recall < 0.99 {
		t.Errorf("found %d of %d planted pairs, recall %.4f; want 0.99 or more", len(lib.planted)-missed, len(lib.planted), recall)
	}
	var decoys []float64
	decoysFound := 0
	for _, p := range lib.decoys {
		decoys = append(decoys, jaccard(set(p[0]), set(p[1])))
		if found[p] {
			decoysFound++
		}
	}

	var trueOfFound []float64
	for _, m := range got {
		j := jaccard(set(m.A), set(m.B))
		trueOfFound = append(trueOfFound, j)
		if j < 0.6 {
			t.Errorf("pair %v: true similarity %.3f; nothing under 0.6 may match", m, j)
		}
	}

	// Find against comparing every pair: the bands should lose almost nothing.
	every, lost := 0, 0
	for a := range sigs {
		for b := a + 1; b < len(sigs); b++ {
			if similarity(&sigs[a], &sigs[b]) >= 0.8 {
				every++
				if !found[[2]int{a, b}] {
					lost++
				}
			}
		}
	}
	if every != len(got)+lost {
		t.Errorf("Find returned %d pairs; every-pair comparison finds %d, of which Find lost %d", len(got), every, lost)
	}

	// Drafts group with their own family and nothing else.
	for _, g := range Groups(got) {
		for _, i := range g[1:] {
			if lib.family[i] != lib.family[g[0]] {
				t.Errorf("group %v joins documents of different families", g)
				break
			}
		}
	}

	t.Logf("planted %d pairs, true similarity %s; found %d, recall %.4f", len(lib.planted), spread(planted), len(lib.planted)-missed, recall)
	t.Logf("decoys %d pairs, true similarity %s; found %d", len(lib.decoys), spread(decoys), decoysFound)
	t.Logf("returned %d pairs, true similarity %s; every-pair comparison finds %d, bands lost %d", len(got), spread(trueOfFound), every, lost)
}

// TestFindFollowsTheSCurve checks the bands against their arithmetic, on pairs of
// signatures that agree on exactly k of their 128 values, against the exact rate
// by inclusion–exclusion over the bands. Measured on 4,000 pairs each:
//
//	agree   51 (0.40)  77 (0.60)  90 (0.70)  103 (0.80)  108 (0.84)
//	exact   0.068      0.645      0.958      0.99997     1
//	found   0.074      0.647      0.951      1           1
func TestFindFollowsTheSCurve(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	const trials = 4000
	for _, c := range []struct {
		k     int
		exact float64 // the chance of becoming a candidate
	}{{51, 0.0683}, {77, 0.6449}, {90, 0.9577}, {103, 0.99997}, {108, 1}} {
		hits := 0
		for range trials {
			var a Signature
			for i := range a.mins {
				a.mins[i] = r.Uint64()
			}
			b := a
			for _, i := range r.Perm(numHashes)[c.k:] {
				b.mins[i] = r.Uint64()
			}
			hits += len(Find([]Signature{a, b}, 0))
		}
		rate := float64(hits) / trials
		t.Logf("agreeing on %d of 128 (%.3f): candidates %.4f, exact %.5f", c.k, float64(c.k)/numHashes, rate, c.exact)
		if sd := math.Sqrt(c.exact * (1 - c.exact) / trials); math.Abs(rate-c.exact) > 4*sd+0.001 {
			t.Errorf("agreeing on %d of 128: candidates %.4f, want %.4f", c.k, rate, c.exact)
		}
	}
}

// TestFindCatchesEveryPairAt108: twenty disagreements, however they fall, leave
// one of the 21 bands whole. Twenty-one, one to a band, leave none, so 108 of 128
// is the tightest such bound.
func TestFindCatchesEveryPairAt108(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	var a Signature
	for i := range a.mins {
		a.mins[i] = r.Uint64()
	}
	spoiled := func(n int) Signature { // one disagreement in each of the first n bands
		b := a
		for band := range n {
			b.mins[band*rows+r.Intn(rows)] ^= 1
		}
		return b
	}
	if m := Find([]Signature{a, spoiled(20)}, 0.8); len(m) != 1 {
		t.Error("108 of 128 agree, 20 bands spoiled: not found")
	}
	if m := Find([]Signature{a, spoiled(21)}, 0); len(m) != 0 {
		t.Error("all 21 bands spoiled: found, yet no band agrees whole")
	}
}

func TestGroupsJoinsTransitively(t *testing.T) {
	got := Groups([]Match{{3, 9, 0.9}, {1, 4, 0.9}, {4, 9, 0.85}, {2, 8, 1}, {6, 7, 0.8}})
	want := [][]int{{1, 3, 4, 9}, {2, 8}, {6, 7}}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("Groups = %v, want %v", got, want)
	}
}

// TestGroupsIgnoresMatchOrder: a chain given backwards, then shuffled, is one set.
func TestGroupsIgnoresMatchOrder(t *testing.T) {
	var chain []Match
	for i := 99; i >= 0; i-- {
		chain = append(chain, Match{A: i, B: i + 1, Score: 0.8})
	}
	chain = append(chain, Match{A: 200, B: 150, Score: 0.9})
	want := [][]int{make([]int, 101), {150, 200}}
	for i := range want[0] {
		want[0][i] = i
	}
	r := rand.New(rand.NewSource(1))
	for range 5 {
		if got := Groups(chain); !slices.EqualFunc(got, want, slices.Equal) {
			t.Fatalf("Groups = %v, want 0 through 100, then 150 and 200", got)
		}
		r.Shuffle(len(chain), func(i, j int) { chain[i], chain[j] = chain[j], chain[i] })
	}
	if got := Groups(nil); len(got) != 0 {
		t.Errorf("Groups(nil) = %v, want none", got)
	}
}

// BenchmarkFind: 50,000 signed documents of eight paragraphs, about 530 words,
// with near-duplicates and decoys planted among them as in
// TestFindRecoversPlantedPairs.
func BenchmarkFind(b *testing.B) {
	sigs := signAll(b, newLibrary(50_000, 8, 1).texts)
	b.ReportAllocs()
	var n int
	for b.Loop() {
		n = len(Find(sigs, 0.8))
	}
	b.ReportMetric(float64(n), "matches")
}

// BenchmarkSign: one document of twenty paragraphs.
func BenchmarkSign(b *testing.B) {
	text := newWriter(1, 0).document(20).text()
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	for b.Loop() {
		Sign(text)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(wordCount(text)), "ns/word")
}
