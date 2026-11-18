package configsync_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JLugagne/aw/internal/config"
	"github.com/JLugagne/aw/internal/configsync"
)

type machine struct {
	t      *testing.T
	config string
	syncer *configsync.Syncer
}

func gitEnv(t *testing.T) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[init]\n\tdefaultBranch = main\n[user]\n\tname = test\n\temail = test@example.invalid\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func newRemote(t *testing.T) string {
	t.Helper()
	gitEnv(t)
	dir := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return dir
}

func newMachine(t *testing.T, remote, host string) *machine {
	t.Helper()
	root := t.TempDir()
	m := &machine{t: t, config: filepath.Join(root, "config.yaml")}
	m.syncer = &configsync.Syncer{
		StateDir:   filepath.Join(root, "state"),
		Settings:   config.Sync{Remote: remote},
		ConfigPath: m.config,
		Host:       host,
	}
	return m
}

func (m *machine) write(content string) {
	m.t.Helper()
	if err := os.WriteFile(m.config, []byte(content), 0o644); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) load() *config.Config {
	m.t.Helper()
	c, err := config.Load(m.config)
	if err != nil {
		m.t.Fatal(err)
	}
	return c
}

func (m *machine) addSession(id string) {
	m.t.Helper()
	c := m.load()
	if _, err := c.AddSession(id, ""); err != nil {
		m.t.Fatal(err)
	}
	if err := c.Save(m.config); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) sync(resolve configsync.Resolver) configsync.Result {
	m.t.Helper()
	if resolve == nil {
		resolve = func(c configsync.Conflict) (configsync.Choice, error) {
			m.t.Fatalf("unexpected conflict on %s", c.Key)
			return 0, nil
		}
	}
	r, err := m.syncer.Sync(context.Background(), "test", resolve)
	if err != nil {
		m.t.Fatal(err)
	}
	return r
}

func TestFirstPushThenPull(t *testing.T) {
	remote := newRemote(t)
	a := newMachine(t, remote, "laptop")
	b := newMachine(t, remote, "desktop")
	a.write("base: ~/src\nsessions:\n  s1:\n    name: one\n")

	r := a.sync(nil)
	if !r.Pushed || r.Revision.Number != 1 || r.Revision.Host != "laptop" {
		t.Fatalf("first push: %+v", r)
	}
	r = b.sync(nil)
	if !r.Pulled || r.Revision.Number != 1 {
		t.Fatalf("first pull: %+v", r)
	}
	if b.load().Sessions["s1"].Name != "one" {
		t.Fatal("pulled config missing session")
	}
	if r := b.sync(nil); r.Pulled || r.Pushed || !r.Status.InSync() {
		t.Fatalf("second sync should be a no-op: %+v", r)
	}
}

func TestOneSidedChanges(t *testing.T) {
	remote := newRemote(t)
	a := newMachine(t, remote, "laptop")
	b := newMachine(t, remote, "desktop")
	a.write("sessions:\n  s1: {}\n")
	a.sync(nil)
	b.sync(nil)

	a.addSession("s2")
	st, err := a.syncer.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.LocalChanged || st.RemoteChanged {
		t.Fatalf("status before push: %+v", st)
	}
	if r := a.sync(nil); !r.Pushed || r.Revision.Number != 2 {
		t.Fatalf("push: %+v", r)
	}

	st, err = b.syncer.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.LocalChanged || !st.RemoteChanged || st.Remote.Number != 2 || st.Base.Number != 1 {
		t.Fatalf("remote change not detected: %+v", st)
	}
	if r := b.sync(nil); !r.Pulled {
		t.Fatalf("pull: %+v", r)
	}
	if _, ok := b.load().Sessions["s2"]; !ok {
		t.Fatal("s2 not pulled")
	}
}

func TestConcurrentChangesMergeBySession(t *testing.T) {
	remote := newRemote(t)
	a := newMachine(t, remote, "laptop")
	b := newMachine(t, remote, "desktop")
	a.write("sessions:\n  s1: {}\n")
	a.sync(nil)
	b.sync(nil)

	a.addSession("from-a")
	b.addSession("from-b")
	a.sync(nil)
	r := b.sync(nil)
	if !r.Pushed || r.Revision.Number != 3 || len(r.Report) == 0 {
		t.Fatalf("merge push: %+v", r)
	}
	a.sync(nil)
	for _, m := range []*machine{a, b} {
		c := m.load()
		for _, id := range []string{"s1", "from-a", "from-b"} {
			if _, ok := c.Sessions[id]; !ok {
				t.Fatalf("%s missing on %s", id, m.syncer.Host)
			}
		}
	}
}

func TestConflictOnSameSession(t *testing.T) {
	remote := newRemote(t)
	a := newMachine(t, remote, "laptop")
	b := newMachine(t, remote, "desktop")
	a.write("sessions:\n  s1:\n    name: base\n")
	a.sync(nil)
	b.sync(nil)

	a.write("sessions:\n  s1:\n    name: from-a\n")
	a.sync(nil)
	b.write("sessions:\n  s1:\n    name: from-b\n")
	var keys []string
	r := b.sync(func(c configsync.Conflict) (configsync.Choice, error) {
		keys = append(keys, c.Key)
		if !strings.Contains(string(c.Base), "base") {
			t.Fatalf("base side missing: %q", c.Base)
		}
		return configsync.KeepLocal, nil
	})
	if len(keys) != 1 || keys[0] != "session s1" || !r.Pushed {
		t.Fatalf("conflict keys=%v result=%+v", keys, r)
	}
	a.sync(nil)
	if a.load().Sessions["s1"].Name != "from-b" {
		t.Fatal("kept local side not propagated")
	}
}

func TestConflictAbortLeavesEverythingUntouched(t *testing.T) {
	remote := newRemote(t)
	a := newMachine(t, remote, "laptop")
	b := newMachine(t, remote, "desktop")
	a.write("tool: x\n")
	a.sync(nil)
	b.sync(nil)
	a.write("tool: a\n")
	a.sync(nil)
	b.write("tool: b\n")
	abort := errors.New("abort")
	_, err := b.syncer.Sync(context.Background(), "test", func(configsync.Conflict) (configsync.Choice, error) { return 0, abort })
	if !errors.Is(err, abort) {
		t.Fatalf("want abort, got %v", err)
	}
	if b.load().Tool != "b" {
		t.Fatal("local config changed despite abort")
	}
	st, err := b.syncer.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.LocalChanged || !st.RemoteChanged {
		t.Fatalf("desync forgotten after abort: %+v", st)
	}
}

func TestInvalidRemoteContentIsRejected(t *testing.T) {
	remote := newRemote(t)
	a := newMachine(t, remote, "laptop")
	a.write("tool: x\n")
	a.sync(nil)

	work := filepath.Join(t.TempDir(), "work")
	for _, args := range [][]string{
		{"clone", "-q", remote, work},
		{"-C", work, "rm", "-q", "aw.yaml"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(work, "aw.yaml"), []byte("not: [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-C", work, "add", "aw.yaml"}, {"-C", work, "commit", "-qm", "broken"}, {"-C", work, "push", "-q", "origin", "HEAD"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	if _, err := a.syncer.Sync(context.Background(), "test", nil); err == nil {
		t.Fatal("expected an error for an invalid remote config")
	}
	if a.load().Tool != "x" {
		t.Fatal("local config overwritten by invalid remote content")
	}
}

func TestNotConfigured(t *testing.T) {
	gitEnv(t)
	m := newMachine(t, "", "laptop")
	if _, err := m.syncer.Status(context.Background()); !errors.Is(err, configsync.ErrNotConfigured) {
		t.Fatalf("want ErrNotConfigured, got %v", err)
	}
}

func TestRemoteChangeResetsState(t *testing.T) {
	r1 := newRemote(t)
	r2 := newRemote(t)
	a := newMachine(t, r1, "laptop")
	a.write("tool: x\n")
	a.sync(nil)
	a.syncer.Settings.Remote = r2
	r := a.sync(nil)
	if !r.Pushed || r.Revision.Number != 1 {
		t.Fatalf("new remote not initialised: %+v", r)
	}
}

func TestFirstSyncTakesRemoteSettingsAndMergesSessions(t *testing.T) {
	remote := newRemote(t)
	a := newMachine(t, remote, "laptop")
	b := newMachine(t, remote, "desktop")
	a.write("base: /a/src\nsessions:\n  from-a: {}\n  same:\n    name: x\n")
	a.sync(nil)
	b.write("base: /b/src\nsessions:\n  from-b: {}\n  same:\n    name: x\n")
	r := b.sync(nil)
	if !r.Pushed {
		t.Fatalf("merged result not pushed: %+v", r)
	}
	c := b.load()
	if c.Base != "/a/src" {
		t.Fatalf("remote settings should win on first sync, base = %q", c.Base)
	}
	for _, id := range []string{"from-a", "from-b", "same"} {
		if _, ok := c.Sessions[id]; !ok {
			t.Fatalf("%s missing after first sync", id)
		}
	}
}

func TestPushIgnoresCommitSigning(t *testing.T) {
	remote := newRemote(t)
	cfg := os.Getenv("GIT_CONFIG_GLOBAL")
	f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = false\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	a := newMachine(t, remote, "laptop")
	a.write("tool: x\n")
	if r := a.sync(nil); !r.Pushed {
		t.Fatalf("not pushed: %+v", r)
	}
}

func TestInsteadOfRewriteDoesNotRecloneEverySync(t *testing.T) {
	remote := newRemote(t)
	cfg := os.Getenv("GIT_CONFIG_GLOBAL")
	f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("[url \"" + remote + "\"]\n\tinsteadOf = aw-test:remote\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	a := newMachine(t, "aw-test:remote", "laptop")
	a.write("tool: x\n")
	a.sync(nil)
	marker := filepath.Join(a.syncer.StateDir, "repo", ".git", "aw-test-marker")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	a.sync(nil)
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the sync clone was deleted and cloned again")
	}
}
