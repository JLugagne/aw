package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JLugagne/aw/internal/cli"
	"github.com/JLugagne/aw/internal/config"
)

type launched struct {
	path string
	argv []string
	dir  string
	env  []string
}

type env struct {
	t        *testing.T
	root     string
	cfgPath  string
	launches []launched
}

func setup(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	gitcfg := filepath.Join(root, "gitconfig")
	if err := os.WriteFile(gitcfg, []byte("[init]\n\tdefaultBranch = main\n[protocol \"file\"]\n\tallow = always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gitcfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")

	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "opencode", "myeditor"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	e := &env{t: t, root: root, cfgPath: filepath.Join(root, "config", "aw.yaml")}
	cfg := &config.Config{Base: filepath.Join(root, "base"), Workspaces: filepath.Join(root, "ws"), Editor: "myeditor --wait"}
	if err := cfg.Save(e.cfgPath); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) git(dir string, args ...string) string {
	e.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (e *env) remote(name string) (string, string) {
	e.t.Helper()
	bare := filepath.Join(e.root, "remotes", name+".git")
	e.git(e.root, "init", "--bare", "-b", "main", bare)
	work := filepath.Join(e.root, "work", name)
	e.git(e.root, "clone", bare, work)
	e.commit(work, "README")
	e.git(work, "push", "origin", "main")
	return "file://" + bare, work
}

func (e *env) commit(dir, file string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(file), 0o644); err != nil {
		e.t.Fatal(err)
	}
	e.git(dir, "add", file)
	e.git(dir, "commit", "-m", file)
}

func (e *env) run(input string, interactive bool, args ...string) (string, string, error) {
	e.t.Helper()
	var out, errOut bytes.Buffer
	a := &cli.App{
		Version:     "test",
		In:          strings.NewReader(input),
		Out:         &out,
		Err:         &errOut,
		Interactive: interactive,
		Exec: func(path string, argv []string, dir string, environ []string) error {
			e.launches = append(e.launches, launched{path, argv, dir, environ})
			return nil
		},
	}
	root := a.Root()
	root.SetArgs(append([]string{"--config", e.cfgPath}, args...))
	err := root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func (e *env) mustRun(args ...string) string {
	e.t.Helper()
	out, stderr, err := e.run("", false, args...)
	if err != nil {
		e.t.Fatalf("aw %v: %v\n%s", args, err, stderr)
	}
	return out
}

func (e *env) config() *config.Config {
	e.t.Helper()
	c, err := config.Load(e.cfgPath)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func TestCreateAddAndList(t *testing.T) {
	e := setup(t)
	urlA, _ := e.remote("alpha")
	urlB, _ := e.remote("beta")
	e.mustRun("create", "s1", "--name", "Session One", urlA+"@feature_1")
	e.mustRun("add", "s1", urlB, urlA+"@feature_1")

	s := e.config().Sessions["s1"]
	if s.Name != "Session One" || len(s.Projects) != 2 || s.Projects[0].Branch != "feature_1" || s.Projects[1].Branch != "" {
		t.Fatalf("unexpected session %+v", s)
	}

	var list []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Exists   bool   `json:"exists"`
		Projects []any  `json:"projects"`
	}
	if err := json.Unmarshal([]byte(e.mustRun("ls", "--json")), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "s1" || list[0].Exists || len(list[0].Projects) != 2 {
		t.Fatalf("unexpected ls output %+v", list)
	}
	if out := e.mustRun("ls"); !strings.Contains(out, "Session One") {
		t.Fatalf("table output missing name:\n%s", out)
	}

	if _, _, err := e.run("", false, "create", "s1"); !errors.Is(err, config.ErrSessionExists) {
		t.Fatalf("want ErrSessionExists, got %v", err)
	}
}

func TestRunLaunchesToolWithAddDir(t *testing.T) {
	e := setup(t)
	urlA, _ := e.remote("alpha")
	urlB, _ := e.remote("beta")
	e.mustRun("create", "s1", urlA, urlB)
	e.mustRun("run", "s1", "--", "--continue")

	if len(e.launches) != 1 {
		t.Fatalf("expected one launch, got %d", len(e.launches))
	}
	l := e.launches[0]
	sessionDir := filepath.Join(e.root, "ws", "s1")
	want := []string{"claude", "--add-dir", filepath.Join(sessionDir, "alpha"), "--add-dir", filepath.Join(sessionDir, "beta"), "--continue"}
	if !slices.Equal(l.argv, want) || l.dir != sessionDir {
		t.Fatalf("argv=%v dir=%s", l.argv, l.dir)
	}
	if !slices.Contains(l.env, "AW_SESSION=s1") {
		t.Fatal("AW_SESSION not exported")
	}

	e.mustRun("run", "--tool", "opencode", "s1")
	if got := e.launches[1].argv; !slices.Equal(got, []string{"opencode"}) {
		t.Fatalf("opencode argv = %v", got)
	}
}

func TestEditOpensConfiguredEditor(t *testing.T) {
	e := setup(t)
	url, _ := e.remote("alpha")
	e.mustRun("create", "s1", url)
	e.mustRun("edit", "s1")
	sessionDir := filepath.Join(e.root, "ws", "s1")
	if got := e.launches[0].argv; !slices.Equal(got, []string{"myeditor", "--wait", sessionDir}) {
		t.Fatalf("editor argv = %v", got)
	}
	e.mustRun("edit", "--editor", "claude", "s1")
	if got := e.launches[1].argv; got[0] != "claude" {
		t.Fatalf("override ignored: %v", got)
	}
}

func TestFuzzySessionLookup(t *testing.T) {
	e := setup(t)
	url, _ := e.remote("alpha")
	e.mustRun("create", "session1", url)

	_, _, err := e.run("", false, "run", "sesion1")
	if !errors.Is(err, cli.ErrSessionNotFound) || !strings.Contains(err.Error(), "did you mean: session1") {
		t.Fatalf("want suggestion, got %v", err)
	}
	if len(e.launches) != 0 {
		t.Fatal("launched despite unknown session")
	}

	if _, stderr, err := e.run("y\n", true, "run", "--no-pull", "sesion1"); err != nil {
		t.Fatalf("interactive guess failed: %v\n%s", err, stderr)
	}
	if len(e.launches) != 1 || !slices.Contains(e.launches[0].env, "AW_SESSION=session1") {
		t.Fatalf("guess not applied: %+v", e.launches)
	}

	if _, _, err := e.run("y\n", true, "remove", "sesion1"); !errors.Is(err, cli.ErrSessionNotFound) {
		t.Fatalf("remove must never guess, got %v", err)
	}
}

func TestRunOffersPull(t *testing.T) {
	e := setup(t)
	url, work := e.remote("alpha")
	e.mustRun("create", "s1", url)
	e.mustRun("run", "s1")
	e.commit(work, "NEW")
	e.git(work, "push", "origin", "main")

	worktree := filepath.Join(e.root, "ws", "s1", "alpha")
	if _, stderr, err := e.run("n\n", true, "run", "s1"); err != nil || !strings.Contains(stderr, "1 behind origin/main") {
		t.Fatalf("behind not reported: %v\n%s", err, stderr)
	}
	if _, err := os.Stat(filepath.Join(worktree, "NEW")); err == nil {
		t.Fatal("pulled although the user said no")
	}
	if _, stderr, err := e.run("y\n", true, "run", "s1"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if _, err := os.Stat(filepath.Join(worktree, "NEW")); err != nil {
		t.Fatal("not pulled although the user said yes")
	}
}

func TestRemoveWarnsAboutLocalWork(t *testing.T) {
	e := setup(t)
	url, _ := e.remote("alpha")
	e.mustRun("create", "s1", url)
	e.mustRun("run", "s1")
	worktree := filepath.Join(e.root, "ws", "s1", "alpha")
	if err := os.WriteFile(filepath.Join(worktree, "WIP"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := e.run("", false, "remove", "-y", "s1")
	if !errors.Is(err, cli.ErrAborted) || !strings.Contains(stderr, "1 uncommitted") {
		t.Fatalf("expected abort with warning, got %v\n%s", err, stderr)
	}
	if _, _, err := e.run("n\n", true, "remove", "s1"); !errors.Is(err, cli.ErrAborted) {
		t.Fatalf("expected abort on 'n', got %v", err)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatal("worktree deleted after abort")
	}

	if _, stderr, err := e.run("y\n", true, "remove", "s1"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if _, err := os.Stat(filepath.Join(e.root, "ws", "s1")); !os.IsNotExist(err) {
		t.Fatal("session dir still present")
	}
	if _, ok := e.config().Sessions["s1"]; ok {
		t.Fatal("session still in config")
	}
}

func TestRemoveCleanSessionWithYes(t *testing.T) {
	e := setup(t)
	url, _ := e.remote("alpha")
	e.mustRun("create", "s1", url)
	e.mustRun("run", "s1")
	e.mustRun("remove", "-y", "--keep-config", "s1")
	if _, ok := e.config().Sessions["s1"]; !ok {
		t.Fatal("--keep-config removed the session")
	}
	if _, err := os.Stat(filepath.Join(e.root, "ws", "s1")); !os.IsNotExist(err) {
		t.Fatal("session dir still present")
	}
}

func TestDetach(t *testing.T) {
	e := setup(t)
	urlA, _ := e.remote("alpha")
	urlB, _ := e.remote("beta")
	e.mustRun("create", "s1", urlA, urlB)
	e.mustRun("run", "s1")
	e.mustRun("detach", "-y", "s1", "alpha")

	s := e.config().Sessions["s1"]
	if len(s.Projects) != 1 || s.Projects[0].Repo != urlB {
		t.Fatalf("unexpected projects %+v", s.Projects)
	}
	if _, err := os.Stat(filepath.Join(e.root, "ws", "s1", "alpha")); !os.IsNotExist(err) {
		t.Fatal("worktree still present")
	}
	if _, _, err := e.run("", false, "detach", "-y", "s1", "nope"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestPathAndShowJSON(t *testing.T) {
	e := setup(t)
	url, _ := e.remote("alpha")
	e.mustRun("create", "s1", url)
	if got := strings.TrimSpace(e.mustRun("path", "s1")); got != filepath.Join(e.root, "ws", "s1") {
		t.Fatalf("path = %q", got)
	}
	e.mustRun("run", "s1")
	var show struct {
		ID       string `json:"id"`
		Projects []struct {
			Branch string `json:"branch"`
			Dir    string `json:"dir"`
			Status struct {
				Behind int `json:"behind"`
			} `json:"status"`
		} `json:"projects"`
	}
	if err := json.Unmarshal([]byte(e.mustRun("show", "--json", "s1")), &show); err != nil {
		t.Fatal(err)
	}
	if show.ID != "s1" || len(show.Projects) != 1 || show.Projects[0].Branch != "s1" || show.Projects[0].Dir == "" {
		t.Fatalf("unexpected show output %+v", show)
	}
}

func TestAddChecksOutWhenWorkspaceExists(t *testing.T) {
	e := setup(t)
	urlA, _ := e.remote("alpha")
	urlB, _ := e.remote("beta")
	e.mustRun("create", "s1", urlA)
	e.mustRun("add", "s1", urlB+"@feat")
	if _, err := os.Stat(filepath.Join(e.root, "ws", "s1")); !os.IsNotExist(err) {
		t.Fatal("add created a workspace that did not exist yet")
	}
	e.mustRun("sync", "s1")
	urlC, _ := e.remote("gamma")
	e.mustRun("add", "s1", urlC)
	if got := e.git(filepath.Join(e.root, "ws", "s1", "gamma"), "branch", "--show-current"); got != "s1" {
		t.Fatalf("gamma worktree branch = %q", got)
	}
	urlD, _ := e.remote("delta")
	e.mustRun("add", "--no-checkout", "s1", urlD)
	if _, err := os.Stat(filepath.Join(e.root, "ws", "s1", "delta")); !os.IsNotExist(err) {
		t.Fatal("--no-checkout created the worktree")
	}
}

func TestSyncJSONDoesNotLaunch(t *testing.T) {
	e := setup(t)
	url, _ := e.remote("alpha")
	e.mustRun("create", "s1", url)
	var out struct {
		Projects []struct {
			Dir string `json:"dir"`
		} `json:"projects"`
	}
	if err := json.Unmarshal([]byte(e.mustRun("sync", "--json", "s1")), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Projects) != 1 || out.Projects[0].Dir == "" || len(e.launches) != 0 {
		t.Fatalf("unexpected sync result %+v, launches=%d", out, len(e.launches))
	}
}

func TestNonInteractiveRefusalExplainsFlags(t *testing.T) {
	e := setup(t)
	url, _ := e.remote("alpha")
	e.mustRun("create", "s1", url)
	e.mustRun("sync", "s1")
	_, _, err := e.run("", false, "remove", "s1")
	if !errors.Is(err, cli.ErrAborted) || !strings.Contains(err.Error(), "-y") {
		t.Fatalf("want an actionable abort, got %v", err)
	}
}
