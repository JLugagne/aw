package git_test

import (
	"context"
	"testing"

	"github.com/JLugagne/aw/internal/git"
)

func TestValidBranch(t *testing.T) {
	ctx := context.Background()
	for _, b := range []string{"main", "feature/x", "fix_1"} {
		if err := (git.Git{}).ValidBranch(ctx, b); err != nil {
			t.Errorf("%q: %v", b, err)
		}
	}
	for _, b := range []string{"", "-f", "--orphan", "a..b", "x.lock", "a b"} {
		if err := (git.Git{}).ValidBranch(ctx, b); err == nil {
			t.Errorf("%q: want error", b)
		}
	}
}

func TestRedact(t *testing.T) {
	tests := map[string]string{
		"git clone https://ghp_secret@github.com/a/b": "git clone https://***@github.com/a/b",
		"https://user:pass@gitlab.com/x":              "https://***@gitlab.com/x",
		"git@github.com:a/b.git":                      "git@github.com:a/b.git",
		"ssh://git@host/a":                            "ssh://git@host/a",
	}
	for in, want := range tests {
		if got := git.Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}
