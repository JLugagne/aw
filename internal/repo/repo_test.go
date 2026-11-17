package repo_test

import (
	"errors"
	"testing"

	"github.com/JLugagne/aw/internal/repo"
)

func TestParse(t *testing.T) {
	tests := []struct {
		spec, protocol string
		path, url      string
	}{
		{"github.com/org/name", "", "github.com/org/name", "git@github.com:org/name.git"},
		{"github.com/org/name.git", "ssh", "github.com/org/name", "git@github.com:org/name.git"},
		{"github.com/org/name", "https", "github.com/org/name", "https://github.com/org/name.git"},
		{"gitlab.com/group/sub/name", "", "gitlab.com/group/sub/name", "git@gitlab.com:group/sub/name.git"},
		{"git@github.com:org/name.git", "", "github.com/org/name", "git@github.com:org/name.git"},
		{"https://github.com/org/name.git", "", "github.com/org/name", "https://github.com/org/name.git"},
		{"ssh://git@host.io:2222/org/name.git", "", "host.io/org/name", "ssh://git@host.io:2222/org/name.git"},
		{"file:///srv/git/name.git", "", "local/srv/git/name", "file:///srv/git/name.git"},
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			ref, err := repo.Parse(tt.spec, tt.protocol)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if ref.Path != tt.path || ref.URL != tt.url {
				t.Fatalf("got path=%q url=%q, want path=%q url=%q", ref.Path, ref.URL, tt.path, tt.url)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	for _, spec := range []string{"", "   ", "github.com", "github.com/../etc", "github.com/a/./b", "file:///"} {
		t.Run(spec, func(t *testing.T) {
			if _, err := repo.Parse(spec, ""); !errors.Is(err, repo.ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
	if _, err := repo.Parse("github.com/a/b", "ftp"); !errors.Is(err, repo.ErrInvalid) {
		t.Fatalf("unknown protocol: want ErrInvalid, got %v", err)
	}
}

func TestNameOwner(t *testing.T) {
	ref, err := repo.Parse("github.com/org/name", "")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Name() != "name" || ref.Owner() != "org" {
		t.Fatalf("got name=%q owner=%q", ref.Name(), ref.Owner())
	}
}

func TestSplitSpec(t *testing.T) {
	tests := []struct{ in, repo, branch string }{
		{"github.com/org/name", "github.com/org/name", ""},
		{"github.com/org/name@feat", "github.com/org/name", "feat"},
		{"github.com/org/name@feature/x", "github.com/org/name", "feature/x"},
		{"git@github.com:org/name.git", "git@github.com:org/name.git", ""},
		{"git@github.com:org/name.git@feat", "git@github.com:org/name.git", "feat"},
		{"ssh://git@host/org/name", "ssh://git@host/org/name", ""},
		{"ssh://git@host:22/org/name", "ssh://git@host:22/org/name", ""},
		{"github.com/org/name@", "github.com/org/name@", ""},
	}
	for _, tt := range tests {
		r, b := repo.SplitSpec(tt.in)
		if r != tt.repo || b != tt.branch {
			t.Errorf("SplitSpec(%q) = %q, %q; want %q, %q", tt.in, r, b, tt.repo, tt.branch)
		}
	}
}
