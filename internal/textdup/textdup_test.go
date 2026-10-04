package textdup

import (
	"fmt"
	"hash/fnv"
	"math"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// TestSimilaritySeparatesRevisionsFromOtherDocuments pins the estimate to
// measured behavior. 200 pairs of each kind, twenty paragraphs a document,
// about 1,300 words; the estimate as minimum, median, maximum, the true Jaccard
// similarity beside it, and the share of pairs at 0.8 or more:
//
//	                                   estimated             true                  ≥ 0.8
//	identical                          1.000  1.000  1.000   1.000  1.000  1.000   100%
//	one paragraph edited               0.906  0.969  1.000   0.947  0.967  0.979   100%
//	one paragraph rewritten            0.812  0.898  0.977   0.849  0.899  0.948   100%
//	navigation added, 10% of length    0.828  0.906  0.984   0.895  0.906  0.911   100%
//	paragraphs reordered               0.805  0.898  0.961   0.874  0.897  0.919   100%
//	as HTML, tags and all              0.656  0.750  0.875   0.716  0.754  0.788    12%
//	as HTML, tags stripped             0.898  0.969  0.992   0.957  0.963  0.969   100%
//	different navigation on each, 10%  0.734  0.836  0.906   0.820  0.832  0.841    83%
//	same topic, shared navigation      0.000  0.047  0.102   0.041  0.048  0.058     0%
//	stub pages, navigation 90% of each 0.672  0.812  0.906   0.712  0.808  0.864    54%
//	unrelated                          0.000  0.000  0.000   0.000  0.000  0.001     0%
//
// Three rows are warnings, not goals. Raw HTML loses a quarter of its similarity
// to its own text: every tag is a word, and each paragraph's tags break the
// shingles around them, so a caller should strip tags first. A page saved twice
// with different navigation sits just above 0.8 and is missed one time in six.
// Two stub pages that are mostly the same navigation match half the time, though
// their articles differ: similarity counts shared text, wherever it is.
func TestSimilaritySeparatesRevisionsFromOtherDocuments(t *testing.T) {
	const paragraphs = 20
	doc := func(seed int64, s int) (*writer, doc) {
		w := newWriter(seed, s)
		return w, w.document(paragraphs)
	}
	cases := []struct {
		name string
		want bound
		pair func(seed int64) (a, b string)
	}{
		{"identical", atLeast(1), func(seed int64) (string, string) {
			_, d := doc(seed, int(seed))
			return d.text(), d.text()
		}},
		{"one paragraph edited", atLeast(0.8), func(seed int64) (string, string) {
			w, d := doc(seed, int(seed))
			return d.text(), w.edited(d).text()
		}},
		{"one paragraph rewritten", atLeast(0.75), func(seed int64) (string, string) {
			w, d := doc(seed, int(seed))
			return d.text(), w.rewritten(d).text()
		}},
		{"navigation added, 10% of length", atLeast(0.8), func(seed int64) (string, string) {
			_, d := doc(seed, int(seed))
			text := d.text()
			header, footer := chrome(seed, wordCount(text)/10)
			return text, wrapped(text, header, footer)
		}},
		{"paragraphs reordered", atLeast(0.75), func(seed int64) (string, string) {
			w, d := doc(seed, int(seed))
			return d.text(), w.reordered(d).text()
		}},
		{"as HTML, tags and all", atLeast(0.6), func(seed int64) (string, string) {
			w, d := doc(seed, int(seed))
			title := w.title()
			return title + "\n\n" + d.text(), asHTML(title, d, w.s.slug)
		}},
		{"as HTML, tags stripped", atLeast(0.8), func(seed int64) (string, string) {
			w, d := doc(seed, int(seed))
			title := w.title()
			return title + "\n\n" + d.text(), stripped(asHTML(title, d, w.s.slug))
		}},
		{"different navigation on each, 10%", atLeast(0.7), func(seed int64) (string, string) {
			_, d := doc(seed, int(seed))
			text := d.text()
			n := wordCount(text) / 10
			h1, f1 := chrome(seed, n)
			h2, f2 := chrome(seed+7_000_000, n)
			return wrapped(text, h1, f1), wrapped(text, h2, f2)
		}},
		{"same topic, shared navigation", below(0.4), func(seed int64) (string, string) {
			_, a := doc(seed, int(seed))
			_, b := doc(seed+1_000_000, int(seed))
			header, footer := chrome(seed, wordCount(a.text())/10)
			return wrapped(a.text(), header, footer), wrapped(b.text(), header, footer)
		}},
		{"stub pages, navigation 90% of each", atLeast(0.6), func(seed int64) (string, string) {
			a := strings.Join(newWriter(seed, int(seed)).paragraph(), " ")
			b := strings.Join(newWriter(seed+1_000_000, int(seed)).paragraph(), " ")
			header, footer := chrome(seed, 9*wordCount(a))
			return wrapped(a, header, footer), wrapped(b, header, footer)
		}},
		{"unrelated", below(0.15), func(seed int64) (string, string) {
			_, a := doc(seed, int(seed))
			_, b := doc(seed+1_000_000, int(seed)+1)
			return a.text(), b.text()
		}},
	}

	const seeds = 200
	for _, c := range cases {
		var est, exact []float64
		high := 0
		for seed := int64(1); seed <= seeds; seed++ {
			a, b := c.pair(seed)
			sa, okA := Sign(a)
			sb, okB := Sign(b)
			if !okA || !okB {
				t.Fatalf("%s, seed %d: not signed", c.name, seed)
			}
			s, j := Similarity(sa, sb), jaccard(shingleSet(a), shingleSet(b))
			if !c.want.holds(s) {
				t.Errorf("%s, seed %d: similarity %.3f, want %v (true %.3f)", c.name, seed, s, c.want, j)
			}
			if s >= 0.8 {
				high++
			}
			est, exact = append(est, s), append(exact, j)
		}
		t.Logf("%-34s %s   %s   %3.0f%%", c.name, spread(est), spread(exact), 100*float64(high)/seeds)
	}
}

// bound is the range every estimate of a kind of pair must fall in: [lo, hi).
type bound struct{ lo, hi float64 }

func atLeast(v float64) bound { return bound{v, 2} }
func below(v float64) bound   { return bound{-1, v} }

func (b bound) holds(s float64) bool { return b.lo <= s && s < b.hi }

func (b bound) String() string {
	if b.hi > 1 {
		return fmt.Sprintf("≥ %g", b.lo)
	}
	return fmt.Sprintf("< %g", b.hi)
}

// spread is a sample's minimum, median, and maximum.
func spread(xs []float64) string {
	s := slices.Sorted(slices.Values(xs))
	return fmt.Sprintf("%.3f  %.3f  %.3f", s[0], s[len(s)/2], s[len(s)-1])
}

// TestSimilarityEstimatesJaccard checks the hash functions behave as independent
// ones should: across pairs at every similarity, the estimate's error is centered
// on zero, with the spread √(s(1−s)/128) predicts. Correlated functions would
// widen it.
func TestSimilarityEstimatesJaccard(t *testing.T) {
	var zs []float64
	for seed := int64(1); seed <= 300; seed++ {
		w := newWriter(seed, int(seed))
		a := w.document(20)
		b := slices.Clone(a)
		for k := range int(seed % 21) { // rewrite k paragraphs: true similarity from 1 to 0
			b[k] = w.paragraph()
		}
		sa, _ := Sign(a.text())
		sb, _ := Sign(b.text())
		j := jaccard(shingleSet(a.text()), shingleSet(b.text()))
		if sd := math.Sqrt(j * (1 - j) / numHashes); sd > 0.01 {
			zs = append(zs, (Similarity(sa, sb)-j)/sd)
		}
	}
	var mean, variance float64
	for _, z := range zs {
		mean += z
	}
	mean /= float64(len(zs))
	for _, z := range zs {
		variance += (z - mean) * (z - mean)
	}
	sd := math.Sqrt(variance / float64(len(zs)-1))
	t.Logf("%d pairs: error in standard errors, mean %.3f, spread %.3f", len(zs), mean, sd)
	if math.Abs(mean) > 0.2 || sd < 0.8 || sd > 1.2 {
		t.Errorf("error mean %.3f, spread %.3f; want near 0 and 1", mean, sd)
	}
}

func TestShortTextIsNotSigned(t *testing.T) {
	text := func(n int) string {
		ws := make([]string, n)
		for i := range ws {
			ws[i] = fmt.Sprintf("word%d", i)
		}
		return strings.Join(ws, ", ")
	}
	for _, s := range []string{"", "   \n\t", "—!?…", "<div><span class=\"x\"></span></div>", text(MinWords - 1)} {
		if sig, ok := Sign(s); ok || sig != (Signature{}) {
			t.Errorf("Sign(%.40q) = ok %v, want not signed and the zero Signature", s, ok)
		}
	}
	if _, ok := Sign(text(MinWords)); !ok {
		t.Errorf("%d words were not signed", MinWords)
	}
}

// TestWordsNormalize pins what a word is, and that its hash is plain FNV-1a of the
// lowercased word.
func TestWordsNormalize(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"Hello, WORLD! It's 2026.", []string{"hello", "world", "it", "s", "2026"}},
		{`<p class="lead">Tomatoes&nbsp;&amp; beans</p>`, []string{"p", "class", "lead", "tomatoes", "nbsp", "amp", "beans", "p"}},
		{"ÉTÉ Größe naïve İstanbul", []string{"été", "größe", "naïve", "istanbul"}},
		// A mark joins the word before it, and a stray one is dropped.
		{"cafe\u0301 \u0301orphan", []string{"cafe\u0301", "orphan"}},
		// Hindi's vowel signs are marks.
		{"हिन्दी भाषा", []string{"हिन्दी", "भाषा"}},
		// Without spaces, each ideograph or kana is a word.
		{"中文文本,日本語のテキスト", []string{"中", "文", "文", "本", "日", "本", "語", "の", "テ", "キ", "ス", "ト"}},
		{"abc123 x2中y", []string{"abc123", "x2", "中", "y"}},
		// Invalid UTF-8 separates words.
		{"a\xffb\xc3", []string{"a", "b"}},
		{"   ", nil},
	}
	for _, c := range cases {
		var want []uint64
		for _, s := range c.want {
			h := fnv.New64a()
			h.Write([]byte(s))
			want = append(want, h.Sum64())
		}
		var got []uint64
		w := words{text: c.text}
		for w.next() {
			got = append(got, w.hash)
		}
		if !slices.Equal(got, want) {
			t.Errorf("words(%q): got %d words, want %q", c.text, len(got), c.want)
		}
	}
}

// TestIdeographsStartAt2E80 guards ideograph's shortcut: no Chinese character or
// kana lies below U+2E80.
func TestIdeographsStartAt2E80(t *testing.T) {
	for r := rune(0); r < 0x2e80; r++ {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) {
			t.Errorf("%U is an ideograph below U+2E80", r)
		}
	}
}

// TestSignIgnoresCaseAndPunctuation: the same words, cased and punctuated
// differently, are the same text.
func TestSignIgnoresCaseAndPunctuation(t *testing.T) {
	text := newWriter(1, 0).document(3).text()
	a, _ := Sign(text)
	b, _ := Sign(strings.ToUpper(strings.NewReplacer(" ", " -- ", ".", "!?").Replace(text)))
	if a != b {
		t.Errorf("similarity %.3f; want the same signature", Similarity(a, b))
	}
}

// TestSignIsStable pins a signature to fixed values. A signature must mean the
// same on every run and every machine; a change here changes every signature.
func TestSignIsStable(t *testing.T) {
	text := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 10) +
		"Größe, ÉTÉ, naïve cafe\u0301; 日本語のテキスト, हिन्दी भाषा."
	sig, ok := Sign(text)
	if !ok {
		t.Fatal("not signed")
	}
	h := fnv.New64a()
	for _, m := range sig.mins {
		h.Write(fmt.Appendf(nil, "%016x", m))
	}
	if sig.mins[0] != 0x13fad630bab349e || h.Sum64() != 0xbc49ac7f6295f9b0 {
		t.Errorf("mins[0] = %#x, digest %#x; want 0x13fad630bab349e, 0xbc49ac7f6295f9b0", sig.mins[0], h.Sum64())
	}
}

// TestZeroSignatureMatchesNothing: text too short to sign gives the zero Signature,
// and two of them must not look identical.
func TestZeroSignatureMatchesNothing(t *testing.T) {
	sig, _ := Sign(newWriter(1, 0).document(3).text())
	if s := Similarity(Signature{}, Signature{}); s != 0 {
		t.Errorf("two zero signatures: similarity %v, want 0", s)
	}
	if s := Similarity(sig, Signature{}); s != 0 {
		t.Errorf("a signature and the zero one: similarity %v, want 0", s)
	}
	if m := Find([]Signature{{}, sig, {}, sig, {}}, 0); !slices.Equal(m, []Match{{1, 3, 1}}) {
		t.Errorf("Find = %v, want only the two real signatures", m)
	}
}

func FuzzSign(f *testing.F) {
	f.Add(newWriter(1, 0).document(2).text())
	f.Add(`<p class="x">Tomatoes&nbsp;&amp; beans</p>`)
	f.Add("中文文本 हिन्दी cafe\u0301 \xff\xfe İSTANBUL")
	f.Add(strings.Repeat("a ", MinWords))
	f.Fuzz(func(t *testing.T, text string) {
		sig, ok := Sign(text)
		if !ok {
			if sig != (Signature{}) {
				t.Error("unsigned text gave a non-zero signature")
			}
			return
		}
		if s := Similarity(sig, sig); s != 1 {
			t.Errorf("similarity with itself %v, want 1", s)
		}
		if again, _ := Sign(text); again != sig {
			t.Error("signing twice gave two signatures")
		}
		if spaced, _ := Sign(strings.ReplaceAll(text, " ", " ,\t")); spaced != sig {
			t.Error("more separators changed the signature")
		}
		upper := []byte(text) // ASCII bytes are never part of a longer UTF-8 sequence
		for i, c := range upper {
			if 'a' <= c && c <= 'z' {
				upper[i] = c - 'a' + 'A'
			}
		}
		if shouted, _ := Sign(string(upper)); shouted != sig {
			t.Error("capital letters changed the signature")
		}
	})
}
