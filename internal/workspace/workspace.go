// Package workspace materialises a session as a directory holding one
// independent git repository per project. Each is cloned from the shared
// cache at <base>/<host>/<owner>/<name> with hardlinked objects, then pointed
// at the real remote, so sessions cost little disk or network and any branch
// can be checked out in several sessions at once.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/JLugagne/aw/internal/config"
	"github.com/JLugagne/aw/internal/git"
	"github.com/JLugagne/aw/internal/repo"
)

// Manager is not safe for concurrent use.
type Manager struct {
	Config *config.Config
	Git    git.Git
	// Log receives progress messages; nil discards them.
	Log io.Writer
}

// Entry is the state of one project of a session.
type Entry struct {
	Project config.Project `json:"project"`
	Ref     repo.Ref       `json:"-"`
	// Cache is the shared clone the session repository is created from.
	Cache  string `json:"cache"`
	Branch string `json:"branch"`
	// Dir is the session repository, empty when not checked out yet.
	Dir    string     `json:"dir,omitempty"`
	Status git.Status `json:"status"`
	// Orphan marks a repository found on disk but absent from the config.
	Orphan bool `json:"orphan,omitempty"`
	// Warning is a non-fatal problem, such as a failed fetch.
	Warning string `json:"warning,omitempty"`
	Err     error  `json:"-"`
}

// Present reports whether the session repository exists on disk.
func (e Entry) Present() bool {
	return e.Dir != ""
}

type found struct {
	dir, path, branch string
}

// SessionDir returns the directory of session id.
func (m *Manager) SessionDir(id string) (string, error) {
	ws, err := m.Config.WorkspacesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(ws, id), nil
}

func (m *Manager) logf(format string, args ...any) {
	if m.Log != nil {
		fmt.Fprintf(m.Log, format, args...)
	}
}

func (m *Manager) plan(id string, s *config.Session) ([]Entry, error) {
	base, err := m.Config.BaseDir()
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(s.Projects))
	for _, p := range s.Projects {
		e := Entry{Project: p, Branch: p.Branch}
		if e.Branch == "" {
			e.Branch = id
		}
		e.Ref, e.Err = repo.Parse(p.Repo, m.Config.Protocol)
		if e.Err == nil {
			e.Cache = filepath.Join(base, filepath.FromSlash(e.Ref.Path))
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func (m *Manager) scan(ctx context.Context, sessionDir string) ([]found, error) {
	items, err := os.ReadDir(sessionDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []found
	for _, it := range items {
		dir := filepath.Join(sessionDir, it.Name())
		if !it.IsDir() || !git.IsRepo(dir) {
			continue
		}
		f := found{dir: dir, branch: m.Git.CurrentBranch(ctx, dir)}
		if url, err := m.Git.RemoteURL(ctx, dir); err == nil {
			if ref, err := repo.Parse(url, m.Config.Protocol); err == nil {
				f.path = ref.Path
			}
		}
		out = append(out, f)
	}
	return out, nil
}

func match(entries []Entry, repos []found) []found {
	used := make([]bool, len(repos))
	for _, sameBranch := range []bool{true, false} {
		for i := range entries {
			e := &entries[i]
			if e.Err != nil || e.Present() {
				continue
			}
			for j, r := range repos {
				if !used[j] && r.path == e.Ref.Path && (!sameBranch || r.branch == e.Branch) {
					e.Dir = r.dir
					used[j] = true
					break
				}
			}
		}
	}
	var orphans []found
	for j, w := range repos {
		if !used[j] {
			orphans = append(orphans, w)
		}
	}
	return orphans
}

// Inspect reports the state of every project of the session without creating
// anything. Repositories present on disk but no longer configured are returned
// with Orphan set. With fetch, remote refs are refreshed first; fetch
// failures are reported as warnings.
func (m *Manager) Inspect(ctx context.Context, id string, s *config.Session, fetch bool) ([]Entry, error) {
	sessionDir, err := m.SessionDir(id)
	if err != nil {
		return nil, err
	}
	entries, err := m.plan(id, s)
	if err != nil {
		return nil, err
	}
	repos, err := m.scan(ctx, sessionDir)
	if err != nil {
		return nil, err
	}
	base, err := m.Config.BaseDir()
	if err != nil {
		return nil, err
	}
	for _, o := range match(entries, repos) {
		name := o.path
		if name == "" {
			name = filepath.Base(o.dir)
		}
		entries = append(entries, Entry{
			Project: config.Project{Repo: name, Branch: o.branch},
			Ref:     repo.Ref{Spec: name, Path: name},
			Cache:   filepath.Join(base, filepath.FromSlash(name)),
			Branch:  o.branch,
			Dir:     o.dir,
			Orphan:  true,
		})
	}
	m.refresh(ctx, entries, fetch)
	return entries, nil
}

// Prepare clones missing caches, creates missing session repositories and
// fetches the existing ones (when fetch is set), then reports every
// project's state.
// Per-project failures are stored in Entry.Err so one broken repository does
// not prevent opening the others.
func (m *Manager) Prepare(ctx context.Context, id string, s *config.Session, fetch bool) ([]Entry, error) {
	sessionDir, err := m.SessionDir(id)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return nil, err
	}
	entries, err := m.plan(id, s)
	if err != nil {
		return nil, err
	}
	repos, err := m.scan(ctx, sessionDir)
	if err != nil {
		return nil, err
	}
	match(entries, repos)
	for i := range entries {
		e := &entries[i]
		if e.Err != nil {
			continue
		}
		if e.Present() {
			if fetch {
				m.logf("fetching %s\n", e.Ref.Path)
				if err := m.Git.Fetch(ctx, e.Dir); err != nil {
					e.Warning = "fetch failed: " + errorSummary(err)
				}
			}
			continue
		}
		if !git.IsRepo(e.Cache) {
			if _, statErr := os.Stat(e.Cache); statErr == nil {
				e.Err = errors.New(e.Cache + " exists but is not a git repository")
				continue
			}
			m.logf("cloning %s into %s\n", e.Ref.URL, e.Cache)
			if e.Err = m.Git.Clone(ctx, e.Ref.URL, e.Cache); e.Err != nil {
				continue
			}
		}
		dir := freeDir(sessionDir, *e)
		m.logf("creating %s on branch %s\n", dir, e.Branch)
		if err := m.Git.CloneSession(ctx, e.Cache, e.Ref.URL, dir, e.Branch); err != nil {
			if !errors.Is(err, git.ErrFetch) {
				os.RemoveAll(dir)
				e.Err = err
				continue
			}
			e.Warning = "fetch failed, created from the local cache: " + errorSummary(err)
		}
		e.Dir = dir
	}
	m.refresh(ctx, entries, false)
	return entries, nil
}

func (m *Manager) refresh(ctx context.Context, entries []Entry, fetch bool) {
	for i := range entries {
		e := &entries[i]
		if e.Err != nil || !e.Present() {
			continue
		}
		if fetch {
			if err := m.Git.Fetch(ctx, e.Dir); err != nil {
				e.Warning = "fetch failed: " + errorSummary(err)
			}
		}
		def := ""
		if b, err := m.Git.DefaultBranch(ctx, e.Dir); err == nil {
			def = "origin/" + b
		}
		e.Status, e.Err = m.Git.Status(ctx, e.Dir, def)
	}
}

func freeDir(sessionDir string, e Entry) string {
	branch := strings.ReplaceAll(e.Branch, "/", "-")
	candidates := []string{
		e.Ref.Name(),
		e.Ref.Owner() + "-" + e.Ref.Name(),
		e.Ref.Name() + "-" + branch,
		e.Ref.Owner() + "-" + e.Ref.Name() + "-" + branch,
	}
	for _, c := range candidates {
		dir := filepath.Join(sessionDir, c)
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			return dir
		}
	}
	for n := 2; ; n++ {
		dir := filepath.Join(sessionDir, fmt.Sprintf("%s-%d", e.Ref.Name(), n))
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			return dir
		}
	}
}

// FastForward merges each entry's Base into its branch; entries that cannot
// be fast-forwarded are skipped. Failures are stored in Entry.Warning.
func (m *Manager) FastForward(ctx context.Context, entries []Entry) {
	for i := range entries {
		e := &entries[i]
		if e.Err != nil || !e.Present() || !e.Status.CanFastForward() {
			continue
		}
		m.logf("pulling %s (%s)\n", e.Ref.Path, e.Status.Base)
		if err := m.Git.FastForward(ctx, e.Dir, e.Status.Base); err != nil {
			e.Warning = "pull failed: " + errorSummary(err)
			continue
		}
		if s, err := m.Git.Status(ctx, e.Dir, e.Status.Base); err == nil {
			e.Status = s
		}
	}
}

// Remove deletes the session directory. Uncommitted changes and unpushed
// commits of its repositories are lost; the shared caches are kept.
func (m *Manager) Remove(id string) error {
	sessionDir, err := m.SessionDir(id)
	if err != nil {
		return err
	}
	return os.RemoveAll(sessionDir)
}

func errorSummary(err error) string {
	msg := err.Error()
	lines := strings.Split(msg, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && !strings.HasPrefix(l, "exit status") {
			return l
		}
	}
	return msg
}
