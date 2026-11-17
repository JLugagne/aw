package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JLugagne/aw/internal/config"
)

func TestLoadMissingFile(t *testing.T) {
	c, err := config.Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Base != config.DefaultBase || c.Sessions == nil {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.yaml")
	src := `base: ~/src
tool: claude
sessions:
  s1:
    name: "Session 1"
    projects:
      - repo: github.com/org/a
        branch: feature_1
      - repo: github.com/org/b
`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s := c.Sessions["s1"]
	if s == nil || s.Name != "Session 1" || len(s.Projects) != 2 || s.Projects[0].Branch != "feature_1" {
		t.Fatalf("unexpected session: %+v", s)
	}
	if _, err := c.AddSession("s2", "Two"); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	again, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Sessions) != 2 || again.Sessions["s2"].Name != "Two" {
		t.Fatalf("round trip lost data: %+v", again.Sessions)
	}
}

func TestLoadRejectsUnknownAndDuplicateKeys(t *testing.T) {
	for name, src := range map[string]string{
		"unknown":   "base: x\nbsae: y\n",
		"duplicate": "sessions:\n  a:\n    name: x\n  a:\n    name: y\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.yaml")
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Load(path); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestAddSessionErrors(t *testing.T) {
	c := &config.Config{}
	if _, err := c.AddSession("ok", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddSession("ok", ""); !errors.Is(err, config.ErrSessionExists) {
		t.Fatalf("want ErrSessionExists, got %v", err)
	}
	for _, id := range []string{"", "-x", "a/b", "a..b", "x.lock", "a b"} {
		if _, err := c.AddSession(id, ""); !errors.Is(err, config.ErrInvalidID) {
			t.Errorf("%q: want ErrInvalidID, got %v", id, err)
		}
	}
}

func TestDirsAndTools(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	c := &config.Config{Tools: map[string]config.Tool{"mine": {Args: []string{"--x"}}}}
	base, err := c.BaseDir()
	if err != nil {
		t.Fatal(err)
	}
	if base != filepath.Join(home, "src") {
		t.Fatalf("base = %q", base)
	}
	ws, err := c.WorkspacesDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ws, base) {
		t.Fatalf("workspaces %q not under base %q", ws, base)
	}
	if c.ToolName() != "claude" || c.ResolveTool("claude").AddDirFlag != "--add-dir" {
		t.Fatal("claude builtin not applied")
	}
	if got := c.ResolveTool("mine"); got.Command != "mine" || got.Args[0] != "--x" {
		t.Fatalf("custom tool = %+v", got)
	}
	if got := c.ResolveTool("other"); got.Command != "other" || got.AddDirFlag != "" {
		t.Fatalf("unknown tool = %+v", got)
	}
}

func TestEditorCommand(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "nano")
	c := &config.Config{}
	if got := c.EditorCommand(""); len(got) != 1 || got[0] != "nano" {
		t.Fatalf("env fallback = %v", got)
	}
	c.Editor = "code -n"
	if got := c.EditorCommand(""); len(got) != 2 || got[0] != "code" || got[1] != "-n" {
		t.Fatalf("configured = %v", got)
	}
	if got := c.EditorCommand("nvim"); got[0] != "nvim" {
		t.Fatalf("override = %v", got)
	}
	t.Setenv("EDITOR", "")
	if got := (&config.Config{}).EditorCommand(""); got[0] != "vi" {
		t.Fatalf("default = %v", got)
	}
}

func TestResolveToolMergesBuiltin(t *testing.T) {
	c := &config.Config{Tools: map[string]config.Tool{
		"claude": {Args: []string{"--permission-mode", "plan"}, Env: map[string]string{"FOO": "bar"}},
	}}
	got := c.ResolveTool("claude")
	if got.Command != "claude" || got.AddDirFlag != "--add-dir" || len(got.Args) != 2 {
		t.Fatalf("builtin fields lost: %+v", got)
	}
	if got.Env["CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD"] != "1" || got.Env["FOO"] != "bar" {
		t.Fatalf("env not merged: %v", got.Env)
	}
	if len(got.ContinueArgs) != 1 || got.ContinueArgs[0] != "--continue" {
		t.Fatalf("continue args lost: %v", got.ContinueArgs)
	}
	if builtin := (&config.Config{}).ResolveTool("claude"); builtin.Env["FOO"] != "" {
		t.Fatal("config override leaked into the builtin definition")
	}
}
