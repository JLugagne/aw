// Package fuzzy suggests session ids close to a mistyped one. Ids and names
// are indexed as character trigrams with BM25 so typos, missing characters
// and partial words still match; an edit distance catches transpositions in
// ids too short to share trigrams.
package fuzzy

import (
	"slices"
	"strings"
	"unicode"

	"github.com/JLugagne/bm25"
)

// Candidate is a session that can be suggested.
type Candidate struct {
	ID   string
	Name string
}

const minOverlap = 0.34

// Suggest returns up to limit candidate ids ranked by relevance to query.
// Candidates sharing too few trigrams with query are never returned, so an
// unrelated query yields nil.
func Suggest(query string, candidates []Candidate, limit int) []string {
	if strings.TrimSpace(query) == "" || len(candidates) == 0 || limit <= 0 {
		return nil
	}
	q := trigrams(query)
	corpus := make([]string, len(candidates))
	for i, c := range candidates {
		corpus[i] = c.ID + " " + c.Name
	}
	index, err := bm25.NewBM25Okapi(corpus, trigrams, 1.2, 0.75, nil)
	if err != nil {
		return nil
	}
	scores, err := index.GetScores(q)
	if err != nil {
		return nil
	}
	type hit struct {
		id    string
		score float64
	}
	var hits []hit
	ql := strings.ToLower(query)
	for i, c := range candidates {
		id := strings.ToLower(c.ID)
		contains := strings.Contains(id, ql) || strings.Contains(ql, id)
		dist := distance(ql, id)
		near := dist <= maxDistance(len([]rune(ql)))
		if !contains && !near && overlap(q, trigrams(corpus[i])) < minOverlap {
			continue
		}
		s := scores[i]
		if contains {
			s += 1000
		}
		if near {
			s += 100 / float64(1+dist)
		}
		hits = append(hits, hit{c.ID, s})
	}
	slices.SortStableFunc(hits, func(a, b hit) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		}
		return strings.Compare(a.id, b.id)
	})
	if len(hits) == 0 {
		return nil
	}
	out := make([]string, 0, min(limit, len(hits)))
	for _, h := range hits[:min(limit, len(hits))] {
		out = append(out, h.id)
	}
	return out
}

func trigrams(s string) []string {
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var out []string
	for _, w := range words {
		r := []rune(" " + w + " ")
		for i := 0; i+3 <= len(r); i++ {
			out = append(out, string(r[i:i+3]))
		}
	}
	if len(out) == 0 {
		out = append(out, "   ")
	}
	return out
}

func overlap(query, doc []string) float64 {
	set := make(map[string]struct{}, len(doc))
	for _, d := range doc {
		set[d] = struct{}{}
	}
	n := 0
	for _, q := range query {
		if _, ok := set[q]; ok {
			n++
		}
	}
	return float64(n) / float64(len(query))
}

func maxDistance(n int) int {
	switch {
	case n <= 4:
		return 1
	case n <= 8:
		return 2
	}
	return 3
}

func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	d := make([][]int, len(ra)+1)
	for i := range d {
		d[i] = make([]int, len(rb)+1)
		d[i][0] = i
	}
	for j := range len(rb) + 1 {
		d[0][j] = j
	}
	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(ra)][len(rb)]
}
