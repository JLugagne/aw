package configsync_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/JLugagne/aw/internal/config"
	"github.com/JLugagne/aw/internal/configsync"
)

func parse(t *testing.T, s string) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func noConflict(t *testing.T) configsync.Resolver {
	return func(c configsync.Conflict) (configsync.Choice, error) {
		t.Fatalf("unexpected conflict on %s", c.Key)
		return 0, nil
	}
}

const base = `tool: claude
sessions:
  api:
    projects:
      - repo: github.com/o/api
  old:
    projects:
      - repo: github.com/o/old
`

func TestMergeIndependentChanges(t *testing.T) {
	local := parse(t, base+`  billing:
    projects:
      - repo: github.com/o/billing
`)
	remote := parse(t, `tool: opencode
sessions:
  api:
    projects:
      - repo: github.com/o/api
      - repo: github.com/o/web
`)
	merged, _, err := configsync.Merge(parse(t, base), local, remote, noConflict(t))
	if err != nil {
		t.Fatal(err)
	}
	if merged.Tool != "opencode" {
		t.Fatalf("remote settings change lost: %q", merged.Tool)
	}
	if _, ok := merged.Sessions["billing"]; !ok {
		t.Fatal("local addition lost")
	}
	if _, ok := merged.Sessions["old"]; ok {
		t.Fatal("remote deletion lost")
	}
	if len(merged.Sessions["api"].Projects) != 2 {
		t.Fatalf("remote modification lost: %+v", merged.Sessions["api"])
	}
}

func TestMergeSameChangeOnBothSides(t *testing.T) {
	both := base + `  new:
    name: same
`
	merged, _, err := configsync.Merge(parse(t, base), parse(t, both), parse(t, both), noConflict(t))
	if err != nil {
		t.Fatal(err)
	}
	if merged.Sessions["new"].Name != "same" {
		t.Fatal("identical change lost")
	}
}

func TestMergeConflictResolution(t *testing.T) {
	local := parse(t, strings.Replace(base, "github.com/o/api", "github.com/o/api-local", 1))
	remote := parse(t, strings.Replace(base, "github.com/o/api", "github.com/o/api-remote", 1))
	for _, tt := range []struct {
		choice configsync.Choice
		want   string
	}{{configsync.KeepLocal, "github.com/o/api-local"}, {configsync.TakeRemote, "github.com/o/api-remote"}} {
		var seen []string
		merged, _, err := configsync.Merge(parse(t, base), local, remote, func(c configsync.Conflict) (configsync.Choice, error) {
			seen = append(seen, c.Key)
			if !strings.Contains(string(c.Local), "api-local") || !strings.Contains(string(c.Remote), "api-remote") {
				t.Fatalf("conflict sides missing: %+v", c)
			}
			return tt.choice, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(seen) != 1 || seen[0] != "session api" {
			t.Fatalf("conflicts = %v", seen)
		}
		if got := merged.Sessions["api"].Projects[0].Repo; got != tt.want {
			t.Fatalf("got %q, want %q", got, tt.want)
		}
	}
}

func TestMergeConflictDeletedOnOneSide(t *testing.T) {
	local := parse(t, strings.Replace(base, "github.com/o/old", "github.com/o/old2", 1))
	remote := parse(t, `tool: claude
sessions:
  api:
    projects:
      - repo: github.com/o/api
`)
	merged, _, err := configsync.Merge(parse(t, base), local, remote, func(c configsync.Conflict) (configsync.Choice, error) {
		if c.Key != "session old" || c.Remote != nil {
			t.Fatalf("unexpected conflict %+v", c)
		}
		return configsync.TakeRemote, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := merged.Sessions["old"]; ok {
		t.Fatal("taking the remote deletion kept the session")
	}
}

func TestMergeAbort(t *testing.T) {
	local := parse(t, "tool: a\n")
	remote := parse(t, "tool: b\n")
	abort := errors.New("abort")
	_, _, err := configsync.Merge(parse(t, ""), local, remote, func(configsync.Conflict) (configsync.Choice, error) { return 0, abort })
	if !errors.Is(err, abort) {
		t.Fatalf("want abort, got %v", err)
	}
}

func TestDiff(t *testing.T) {
	got := configsync.Diff([]byte("a\nb\nc\n"), []byte("a\nx\nc\n"))
	want := "  a\n- b\n+ x\n  c\n"
	if got != want {
		t.Fatalf("diff:\n%s\nwant:\n%s", got, want)
	}
}
