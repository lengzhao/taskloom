// Package skillmatch scores discovery candidates without a database: name
// prefix and trigram similarity, lexical overlap, BM25, and vector cosine. Scores
// are comparable only within one query.
package skillmatch

import (
	"math"
	"strings"
	"unicode"
)

// TrigramThreshold is the minimum similarity for a fuzzy name match.
const TrigramThreshold = 0.3

// Name scores a name query: a prefix match always outranks a fuzzy one.
func Name(name, q string) (float64, bool) {
	n, q := fold(name), fold(q)
	if q == "" {
		return 0, false
	}
	if strings.HasPrefix(n, q) {
		return 1, true
	}
	if s := Similarity(n, q); s > TrigramThreshold {
		return s, true
	}
	return 0, false
}

// Text is the searchable text of one candidate.
type Text struct {
	Name, Description string
	Extra             []string // declared when-to-use and examples
}

// Lexical scores exact, prefix, substring and trigram matches on the name
// above matches in the description and declared use.
func Lexical(t Text, q string) (float64, bool) {
	q = fold(q)
	if q == "" {
		return 0, false
	}
	n := fold(t.Name)
	switch {
	case n == q:
		return 1, true
	case strings.HasPrefix(n, q):
		return 0.9, true
	case strings.Contains(n, q):
		return 0.8, true
	}
	best := 0.0
	if s := Similarity(n, q); s > TrigramThreshold {
		best = 0.7 * s
	}
	body := fold(t.Description + "\n" + strings.Join(t.Extra, "\n"))
	if strings.Contains(body, q) {
		best = math.Max(best, 0.6)
	} else if f := tokenCoverage(body, q); f > 0 {
		best = math.Max(best, 0.5*f)
	}
	return best, best > 0
}

// Mentions reports whether the text contains every query token: the fixed
// lexical bonus of a semantic match.
func Mentions(t Text, q string) bool {
	return tokenCoverage(fold(t.Name+"\n"+t.Description+"\n"+strings.Join(t.Extra, "\n")), fold(q)) == 1
}

// BM25 parameters (Okapi defaults); the name counts nameWeight times.
const (
	bm25K1     = 1.2
	bm25B      = 0.75
	nameWeight = 2
)

// BM25 scores every text against q over the corpus docs, scaled so the
// best match is 1; 0 means no query term occurs. Terms are words, and runs
// of CJK characters are split into overlapping bigrams.
func BM25(docs []Text, q string) []float64 {
	scores := make([]float64, len(docs))
	query := map[string]struct{}{}
	for _, t := range terms(q) {
		query[t] = struct{}{}
	}
	if len(query) == 0 || len(docs) == 0 {
		return scores
	}
	tf := make([]map[string]int, len(docs))
	lens := make([]int, len(docs))
	df := map[string]int{}
	total := 0
	for i, d := range docs {
		ts := terms(d.Name)
		var all []string
		for range nameWeight {
			all = append(all, ts...)
		}
		all = append(all, terms(d.Description+"\n"+strings.Join(d.Extra, "\n"))...)
		tf[i] = map[string]int{}
		for _, t := range all {
			if tf[i][t] == 0 {
				df[t]++
			}
			tf[i][t]++
		}
		lens[i] = len(all)
		total += len(all)
	}
	avg := float64(total) / float64(len(docs))
	if avg == 0 {
		return scores
	}
	n := float64(len(docs))
	best := 0.0
	for i := range docs {
		for t := range query {
			f := float64(tf[i][t])
			if f == 0 {
				continue
			}
			idf := math.Log(1 + (n-float64(df[t])+0.5)/(float64(df[t])+0.5))
			scores[i] += idf * f * (bm25K1 + 1) / (f + bm25K1*(1-bm25B+bm25B*float64(lens[i])/avg))
		}
		best = math.Max(best, scores[i])
	}
	if best > 0 {
		for i := range scores {
			scores[i] /= best
		}
	}
	return scores
}

// terms folds s into words, splitting CJK runs into bigrams.
func terms(s string) []string {
	var out []string
	for _, w := range words(fold(s)) {
		var run, other []rune
		flush := func() {
			if len(other) > 0 {
				out = append(out, string(other))
				other = nil
			}
			switch {
			case len(run) == 1:
				out = append(out, string(run))
			case len(run) > 1:
				for i := 0; i+2 <= len(run); i++ {
					out = append(out, string(run[i:i+2]))
				}
			}
			run = nil
		}
		for _, r := range w {
			if cjk(r) {
				if len(other) > 0 {
					out = append(out, string(other))
					other = nil
				}
				run = append(run, r)
			} else {
				if len(run) > 0 {
					flush()
				}
				other = append(other, r)
			}
		}
		flush()
	}
	return out
}

func cjk(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// Cosine similarity in [-1, 1]; 0 when either vector is empty or sizes differ.
func Cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// Semantic combines vector similarity with the lexical bonus; ok is false
// when the cosine distance exceeds maxDistance.
func Semantic(cos float64, mentions bool, maxDistance, bonus float64) (float64, bool) {
	if 1-cos > maxDistance {
		return 0, false
	}
	s := cos
	if mentions {
		s += bonus
	}
	return math.Min(1, s), true
}

// Similarity is the pg_trgm-style ratio of shared to total trigrams of
// whitespace-separated words, each padded with two leading and one trailing space.
func Similarity(a, b string) float64 {
	ta, tb := trigrams(a), trigrams(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	shared := 0
	for t := range ta {
		if _, ok := tb[t]; ok {
			shared++
		}
	}
	return float64(shared) / float64(len(ta)+len(tb)-shared)
}

func trigrams(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, w := range words(s) {
		r := []rune("  " + w + " ")
		for i := 0; i+3 <= len(r); i++ {
			out[string(r[i:i+3])] = struct{}{}
		}
	}
	return out
}

// tokenCoverage is the share of query tokens found in body.
func tokenCoverage(body, q string) float64 {
	tokens := words(q)
	if len(tokens) == 0 {
		return 0
	}
	hit := 0
	for _, t := range tokens {
		if strings.Contains(body, t) {
			hit++
		}
	}
	return float64(hit) / float64(len(tokens))
}

func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func fold(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
