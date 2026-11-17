package fuzzy_test

import (
	"slices"
	"testing"

	"github.com/JLugagne/aw/internal/fuzzy"
)

var candidates = []fuzzy.Candidate{
	{ID: "session1", Name: "Sessions 1"},
	{ID: "session2", Name: "Sessions 2"},
	{ID: "billing-api", Name: "Billing refactor"},
	{ID: "frontend", Name: "Web UI"},
}

func TestSuggest(t *testing.T) {
	tests := []struct {
		query string
		first string
	}{
		{"sesion1", "session1"},
		{"session-2", "session2"},
		{"billing", "billing-api"},
		{"biling", "billing-api"},
		{"refactor", "billing-api"},
		{"frontned", "frontend"},
		{"web", "frontend"},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got := fuzzy.Suggest(tt.query, candidates, 3)
			if len(got) == 0 || got[0] != tt.first {
				t.Fatalf("Suggest(%q) = %v, want %q first", tt.query, got, tt.first)
			}
		})
	}
}

func TestSuggestNothingRelated(t *testing.T) {
	if got := fuzzy.Suggest("zzzqqq", candidates, 3); got != nil {
		t.Fatalf("expected no suggestion, got %v", got)
	}
}

func TestSuggestSingleCandidate(t *testing.T) {
	got := fuzzy.Suggest("sesion", []fuzzy.Candidate{{ID: "session1"}}, 3)
	if !slices.Equal(got, []string{"session1"}) {
		t.Fatalf("got %v", got)
	}
}

func TestSuggestEdgeCases(t *testing.T) {
	if fuzzy.Suggest("", candidates, 3) != nil || fuzzy.Suggest("x", nil, 3) != nil || fuzzy.Suggest("x", candidates, 0) != nil {
		t.Fatal("expected nil")
	}
	if got := fuzzy.Suggest("session", candidates, 1); len(got) != 1 {
		t.Fatalf("limit not applied: %v", got)
	}
}

func TestSuggestTranspositionInShortID(t *testing.T) {
	got := fuzzy.Suggest("dmeo", []fuzzy.Candidate{{ID: "demo", Name: "Demo session"}, {ID: "frontend"}}, 3)
	if !slices.Equal(got, []string{"demo"}) {
		t.Fatalf("got %v", got)
	}
}
