package workspace_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JLugagne/aw/internal/config"
	"github.com/JLugagne/aw/internal/git"
	"github.com/JLugagne/aw/internal/workspace"
)

type fixture struct {
	t    *testing.T
	root string
	mgr  *workspace.Manager
}

func gitEnv(t *testing.T) {
	t.Helper()
	empty := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(empty, []byte("[init]\n\tdefaultBranch = main\n[protocol \"file\"]\n\tallow = always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	gitEnv(t)
	root := t.TempDir()
	cfg := &config.Config{
		Base:       filepath.Join(root, "base"),
		Workspaces: filepath.Join(root, "ws"),
		Sessions:   map[string]*config.Session{},
	}
	return &fixture{t: t, root: root, mgr: &workspace.Manager{Config: cfg}}
}

func (f *fixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *fixture) remote(name string) (string, string) {
	f.t.Helper()
	bare := filepath.Join(f.root, "remotes", name+".git")
	f.git(f.root, "init", "--bare", "-b", "main", bare)
	work := filepath.Join(f.root, "work", name)
	f.git(f.root, "clone", bare, work)
	f.commit(work, "README", "hello")
	f.git(work, "push", "origin", "main")
	return "file://" + bare, work
}

func (f *fixture) commit(dir, file, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git(dir, "add", file)
	f.git(dir, "commit", "-m", "update "+file)
}

func (f *fixture) session(id string, projects ...config.Project) *config.Session {
	s := &config.Session{Projects: projects}
	f.mgr.Config.Sessions[id] = s
	return s
}

func mustOK(t *testing.T, entries []workspace.Entry) {
	t.Helper()
	for _, e := range entries {
		if e.Err != nil {
			t.Fatalf("%s: %v", e.Project.Repo, e.Err)
		}
	}
}

func TestPrepareClonesAndCreatesWorktrees(t *testing.T) {
	f := newFixture(t)
	urlA, _ := f.remote("alpha")
	urlB, _ := f.remote("beta")
	s := f.session("s1", config.Project{Repo: urlA}, config.Project{Repo: urlB, Branch: "feature_1"})
	ctx := context.Background()

	entries, err := f.mgr.Prepare(ctx, "s1", s, true)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, entries)
	if entries[0].Branch != "s1" || entries[0].Status.Branch != "s1" {
		t.Fatalf("default branch should be the session id, got %+v", entries[0])
	}
	if entries[1].Status.Branch != "feature_1" {
		t.Fatalf("explicit branch ignored: %+v", entries[1])
	}
	if filepath.Base(entries[0].Dir) != "alpha" || filepath.Base(entries[1].Dir) != "beta" {
		t.Fatalf("unexpected dirs %q %q", entries[0].Dir, entries[1].Dir)
	}
	if entries[0].Status.Base != "origin/main" || entries[0].Status.Behind != 0 {
		t.Fatalf("unexpected status %+v", entries[0].Status)
	}

	again, err := f.mgr.Prepare(ctx, "s1", s, false)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, again)
	if again[0].Dir != entries[0].Dir {
		t.Fatalf("second prepare moved the worktree: %q != %q", again[0].Dir, entries[0].Dir)
	}
}

func TestBehindAndFastForward(t *testing.T) {
	f := newFixture(t)
	url, work := f.remote("alpha")
	s := f.session("s1", config.Project{Repo: url})
	ctx := context.Background()
	if _, err := f.mgr.Prepare(ctx, "s1", s, true); err != nil {
		t.Fatal(err)
	}

	f.commit(work, "NEW", "x")
	f.git(work, "push", "origin", "main")

	entries, err := f.mgr.Inspect(ctx, "s1", s, true)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, entries)
	if entries[0].Status.Behind != 1 || !entries[0].Status.CanFastForward() {
		t.Fatalf("expected 1 commit behind, got %+v", entries[0].Status)
	}
	f.mgr.FastForward(ctx, entries)
	if entries[0].Warning != "" || entries[0].Status.Behind != 0 {
		t.Fatalf("fast-forward failed: %q %+v", entries[0].Warning, entries[0].Status)
	}
	if _, err := os.Stat(filepath.Join(entries[0].Dir, "NEW")); err != nil {
		t.Fatal("pulled file missing")
	}
}

func TestSameRepoInTwoSessions(t *testing.T) {
	f := newFixture(t)
	url, _ := f.remote("alpha")
	ctx := context.Background()
	e1, err := f.mgr.Prepare(ctx, "s1", f.session("s1", config.Project{Repo: url}), true)
	if err != nil {
		t.Fatal(err)
	}
	e2, err := f.mgr.Prepare(ctx, "s2", f.session("s2", config.Project{Repo: url}), true)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, e1)
	mustOK(t, e2)
	if e1[0].Cache != e2[0].Cache || e1[0].Dir == e2[0].Dir {
		t.Fatalf("expected one shared cache and two session repositories: %+v / %+v", e1[0], e2[0])
	}
}

func TestSameRepoTwiceInOneSession(t *testing.T) {
	f := newFixture(t)
	url, _ := f.remote("alpha")
	s := f.session("s1", config.Project{Repo: url}, config.Project{Repo: url, Branch: "other"})
	entries, err := f.mgr.Prepare(context.Background(), "s1", s, true)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, entries)
	if entries[0].Dir == entries[1].Dir {
		t.Fatal("both projects share a directory")
	}
}

func TestSameBranchInTwoSessions(t *testing.T) {
	f := newFixture(t)
	url, _ := f.remote("alpha")
	ctx := context.Background()
	e1, err := f.mgr.Prepare(ctx, "s1", f.session("s1", config.Project{Repo: url, Branch: "shared"}), true)
	if err != nil {
		t.Fatal(err)
	}
	e2, err := f.mgr.Prepare(ctx, "s2", f.session("s2", config.Project{Repo: url, Branch: "shared"}), true)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, e1)
	mustOK(t, e2)
	if e1[0].Status.Branch != "shared" || e2[0].Status.Branch != "shared" {
		t.Fatalf("branch not checked out in both sessions: %q %q", e1[0].Status.Branch, e2[0].Status.Branch)
	}
}

func TestSessionRepositoryUsesRealRemote(t *testing.T) {
	f := newFixture(t)
	url, work := f.remote("alpha")
	f.git(work, "push", "origin", "main:feature")
	ctx := context.Background()
	entries, err := f.mgr.Prepare(ctx, "s1", f.session("s1", config.Project{Repo: url, Branch: "feature"}), true)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, entries)
	dir := entries[0].Dir
	if got := f.git(dir, "remote", "get-url", "origin"); got != url {
		t.Fatalf("origin = %q, want %q", got, url)
	}
	if up := entries[0].Status.Upstream; up != "origin/feature" {
		t.Fatalf("upstream = %q", up)
	}
	if branches := f.git(dir, "branch", "--format=%(refname:short)"); branches != "feature" {
		t.Fatalf("stray local branches: %q", branches)
	}
	f.commit(dir, "FROM_SESSION", "x")
	f.git(dir, "push", "origin", "feature")
	f.git(work, "fetch", "origin")
	f.git(work, "cat-file", "-e", "origin/feature:FROM_SESSION")
}

func TestCacheOnOtherBranchDoesNotLeak(t *testing.T) {
	f := newFixture(t)
	url, work := f.remote("alpha")
	f.commit(work, "SIDE", "x")
	f.git(work, "push", "origin", "main:side")
	f.git(work, "reset", "--hard", "HEAD~1")
	ctx := context.Background()
	entries, err := f.mgr.Prepare(ctx, "s1", f.session("s1", config.Project{Repo: url}), true)
	if err != nil {
		t.Fatal(err)
	}
	cache := entries[0].Cache
	f.git(cache, "checkout", "-q", "side")
	f.commit(cache, "LOCAL_ONLY", "x")

	entries, err = f.mgr.Prepare(ctx, "s2", f.session("s2", config.Project{Repo: url}), true)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, entries)
	if base := entries[0].Status.Base; base != "origin/main" {
		t.Fatalf("base = %q, want origin/main", base)
	}
	if _, err := os.Stat(filepath.Join(entries[0].Dir, "SIDE")); err == nil {
		t.Fatal("session branch created from the cache's checked-out branch")
	}
	if entries[0].Status.Unpushed != 0 {
		t.Fatalf("cache-only commits leaked: %+v", entries[0].Status)
	}
}

func TestBranchSwitchInsideSessionIsNotOrphaned(t *testing.T) {
	f := newFixture(t)
	url, _ := f.remote("alpha")
	s := f.session("s1", config.Project{Repo: url})
	ctx := context.Background()
	entries, err := f.mgr.Prepare(ctx, "s1", s, true)
	if err != nil {
		t.Fatal(err)
	}
	f.git(entries[0].Dir, "checkout", "-q", "-b", "experiment")
	again, err := f.mgr.Prepare(ctx, "s1", s, false)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, again)
	if len(again) != 1 || again[0].Dir != entries[0].Dir || again[0].Status.Branch != "experiment" {
		t.Fatalf("switched repository not recognised: %+v", again)
	}
}

func TestRemoveDetectsLocalWorkAndCleansUp(t *testing.T) {
	f := newFixture(t)
	url, _ := f.remote("alpha")
	s := f.session("s1", config.Project{Repo: url})
	ctx := context.Background()
	entries, err := f.mgr.Prepare(ctx, "s1", s, true)
	if err != nil {
		t.Fatal(err)
	}
	dir := entries[0].Dir
	f.commit(dir, "LOCAL", "committed")
	if err := os.WriteFile(filepath.Join(dir, "DIRTY"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err = f.mgr.Inspect(ctx, "s1", s, false)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, entries)
	st := entries[0].Status
	if st.Dirty != 1 || st.Unpushed != 1 || !st.HasLocalWork() {
		t.Fatalf("local work not detected: %+v", st)
	}

	if err := f.mgr.Remove("s1"); err != nil {
		t.Fatal(err)
	}
	sessionDir, _ := f.mgr.SessionDir("s1")
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Fatalf("session dir still present: %v", err)
	}
	if !git.IsRepo(entries[0].Cache) {
		t.Fatal("shared cache removed with the session")
	}
}

func TestInspectReportsOrphans(t *testing.T) {
	f := newFixture(t)
	urlA, _ := f.remote("alpha")
	urlB, _ := f.remote("beta")
	s := f.session("s1", config.Project{Repo: urlA}, config.Project{Repo: urlB})
	ctx := context.Background()
	if _, err := f.mgr.Prepare(ctx, "s1", s, true); err != nil {
		t.Fatal(err)
	}
	s.Projects = s.Projects[:1]
	entries, err := f.mgr.Inspect(ctx, "s1", s, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || !entries[1].Orphan || !entries[1].Present() {
		t.Fatalf("expected one orphan, got %+v", entries)
	}
}

func TestLocalWorkOnOtherBranchesAndStashesIsDetected(t *testing.T) {
	f := newFixture(t)
	url, _ := f.remote("alpha")
	s := f.session("s1", config.Project{Repo: url})
	ctx := context.Background()
	entries, err := f.mgr.Prepare(ctx, "s1", s, true)
	if err != nil {
		t.Fatal(err)
	}
	dir := entries[0].Dir
	f.git(dir, "checkout", "-q", "-b", "side")
	f.commit(dir, "SIDE", "x")
	f.git(dir, "checkout", "-q", "s1")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.git(dir, "stash")

	entries, err = f.mgr.Inspect(ctx, "s1", s, false)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, entries)
	st := entries[0].Status
	if st.Unpushed != 1 || st.Stashes != 1 || !st.HasLocalWork() {
		t.Fatalf("local work on other branches or in stashes not detected: %+v", st)
	}
}
