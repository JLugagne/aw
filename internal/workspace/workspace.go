// Package workspace materialises a session as a directory holding one git
// worktree per project. Worktrees share the clone found at
// <base>/<host>/<owner>/<name>, so a repository used by several sessions is
// cloned once; each session checks out its own branch (the session id unless
// the project sets one), as git forbids the same branch in two worktrees.
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
	Clone   string         `json:"clone"`
	Branch  string         `json:"branch"`
	// Dir is the worktree directory, empty when not checked out.
	Dir    string     `json:"dir,omitempty"`
	Status git.Status `json:"status"`
	// Orphan marks a worktree found on disk but absent from the config.
	Orphan bool `json:"orphan,omitempty"`
	// Warning is a non-fatal problem, such as a failed fetch.
	Warning string `json:"warning,omitempty"`
	Err     error  `json:"-"`
}

// Present reports whether the worktree exists on disk.
func (e Entry) Present() bool {
	return e.Dir != ""
}

type found struct {
	dir, clone, branch string
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
			e.Clone = filepath.Join(base, filepath.FromSlash(e.Ref.Path))
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
		clone, branch, err := m.Git.WorktreeInfo(ctx, dir)
		if err != nil {
			continue
		}
		out = append(out, found{dir: dir, clone: clone, branch: branch})
	}
	return out, nil
}

func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

func match(entries []Entry, worktrees []found) []found {
	used := make([]bool, len(worktrees))
	for i := range entries {
		e := &entries[i]
		if e.Err != nil {
			continue
		}
		for j, w := range worktrees {
			if !used[j] && w.branch == e.Branch && samePath(w.clone, e.Clone) {
				e.Dir = w.dir
				used[j] = true
				break
			}
		}
	}
	var orphans []found
	for j, w := range worktrees {
		if !used[j] {
			orphans = append(orphans, w)
		}
	}
	return orphans
}

// Inspect reports the state of every project of the session without creating
// anything. Worktrees present on disk but no longer configured are returned
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
	worktrees, err := m.scan(ctx, sessionDir)
	if err != nil {
		return nil, err
	}
	base, err := m.Config.BaseDir()
	if err != nil {
		return nil, err
	}
	for _, o := range match(entries, worktrees) {
		rel, _ := filepath.Rel(base, o.clone)
		entries = append(entries, Entry{
			Project: config.Project{Repo: filepath.ToSlash(rel), Branch: o.branch},
			Ref:     repo.Ref{Spec: o.clone, Path: filepath.ToSlash(rel)},
			Clone:   o.clone,
			Branch:  o.branch,
			Dir:     o.dir,
			Orphan:  true,
		})
	}
	m.refresh(ctx, entries, fetch)
	return entries, nil
}

// Prepare clones missing repositories, fetches existing ones (when fetch is
// set) and creates missing worktrees, then reports every project's state.
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
	worktrees, err := m.scan(ctx, sessionDir)
	if err != nil {
		return nil, err
	}
	match(entries, worktrees)
	fetched := map[string]bool{}
	for i := range entries {
		e := &entries[i]
		if e.Err != nil {
			continue
		}
		if !git.IsRepo(e.Clone) {
			if _, statErr := os.Stat(e.Clone); statErr == nil {
				e.Err = errors.New(e.Clone + " exists but is not a git repository")
				continue
			}
			m.logf("cloning %s into %s\n", e.Ref.URL, e.Clone)
			if e.Err = m.Git.Clone(ctx, e.Ref.URL, e.Clone); e.Err != nil {
				continue
			}
			fetched[e.Clone] = true
		}
		if fetch && !fetched[e.Clone] {
			fetched[e.Clone] = true
			m.logf("fetching %s\n", e.Ref.Path)
			if err := m.Git.Fetch(ctx, e.Clone); err != nil {
				e.Warning = "fetch failed: " + errorSummary(err)
			}
		}
		if e.Present() {
			continue
		}
		dir := freeDir(sessionDir, *e)
		m.logf("creating worktree %s on branch %s\n", dir, e.Branch)
		if err := m.Git.AddWorktree(ctx, e.Clone, dir, e.Branch); err != nil {
			e.Err = err
			continue
		}
		e.Dir = dir
	}
	m.refresh(ctx, entries, false)
	return entries, nil
}

func (m *Manager) refresh(ctx context.Context, entries []Entry, fetch bool) {
	defaults := map[string]string{}
	fetched := map[string]bool{}
	for i := range entries {
		e := &entries[i]
		if e.Err != nil || !e.Present() {
			continue
		}
		if fetch && !fetched[e.Clone] {
			fetched[e.Clone] = true
			if err := m.Git.Fetch(ctx, e.Clone); err != nil {
				e.Warning = "fetch failed: " + errorSummary(err)
			}
		}
		def, ok := defaults[e.Clone]
		if !ok {
			if b, err := m.Git.DefaultBranch(ctx, e.Clone); err == nil {
				def = "origin/" + b
			}
			defaults[e.Clone] = def
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

// RemoveWorktree deletes the entry's worktree, discarding uncommitted
// changes; the branch is deleted too when deleteBranch is set.
func (m *Manager) RemoveWorktree(ctx context.Context, e Entry, deleteBranch bool) error {
	if !e.Present() {
		return nil
	}
	if err := m.Git.RemoveWorktree(ctx, e.Clone, e.Dir); err != nil {
		return err
	}
	if deleteBranch && e.Branch != "" {
		return m.Git.DeleteBranch(ctx, e.Clone, e.Branch)
	}
	return nil
}

// Remove deletes every worktree of the session, then the session directory.
// Uncommitted changes are lost; commits stay reachable from their branch in
// the shared clone unless deleteBranches is set.
func (m *Manager) Remove(ctx context.Context, id string, deleteBranches bool) error {
	sessionDir, err := m.SessionDir(id)
	if err != nil {
		return err
	}
	worktrees, err := m.scan(ctx, sessionDir)
	if err != nil {
		return err
	}
	var errs []error
	for _, w := range worktrees {
		e := Entry{Clone: w.clone, Dir: w.dir, Branch: w.branch}
		if err := m.RemoveWorktree(ctx, e, deleteBranches); err != nil {
			errs = append(errs, err)
		}
	}
	if err := os.RemoveAll(sessionDir); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
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
