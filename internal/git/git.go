// Package git wraps the git CLI. Every call shells out so the user's
// credentials, ssh agent, hooks and configuration apply unchanged.
package git

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	// ErrNoDefaultBranch is returned by DefaultBranch when origin/HEAD cannot
	// be resolved and neither origin/main nor origin/master exists.
	ErrNoDefaultBranch = errors.New("cannot determine the default branch of origin")
	// ErrFetch marks a failed fetch that did not prevent the operation.
	ErrFetch = errors.New("fetch failed")
)

// Git runs git commands. The zero value is usable.
type Git struct {
	// Progress receives the output of clone; nil discards it.
	Progress io.Writer
}

// Status describes a repository relative to the ref it should track.
type Status struct {
	Branch string `json:"branch"`
	// Upstream is the tracking ref, empty when the branch has none.
	Upstream string `json:"upstream,omitempty"`
	// Base is the ref Ahead and Behind are computed against: Upstream when
	// set, otherwise the remote default branch.
	Base   string `json:"base,omitempty"`
	Ahead  int    `json:"ahead"`
	Behind int    `json:"behind"`
	Dirty  int    `json:"dirty"`
	// Unpushed counts commits of any local branch absent from every
	// remote-tracking ref.
	Unpushed int `json:"unpushed"`
	Stashes  int `json:"stashes"`
}

// CanFastForward reports whether merging Base only moves HEAD forward.
func (s Status) CanFastForward() bool {
	return s.Behind > 0 && s.Ahead == 0
}

// HasLocalWork reports work that is lost when the repository is deleted.
func (s Status) HasLocalWork() bool {
	return s.Dirty > 0 || s.Unpushed > 0 || s.Stashes > 0
}

// Run executes git in dir and returns its trimmed stdout; the error carries
// the command line and stderr.
func (g Git) Run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errs := []error{errors.New("git " + strings.Join(args, " "))}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			errs = append(errs, errors.New(msg))
		}
		return "", errors.Join(append(errs, err)...)
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// IsRepo reports whether dir is the top level of a git working copy.
func IsRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// Clone clones url into dest, creating parent directories.
func (g Git) Clone(ctx context.Context, url, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "clone", url, dest)
	var stderr bytes.Buffer
	out := io.Writer(&stderr)
	if g.Progress != nil {
		out = io.MultiWriter(&stderr, g.Progress)
	}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return errors.Join(errors.New("git clone "+url), errors.New(strings.TrimSpace(stderr.String())), err)
	}
	return nil
}

// Fetch updates the remote-tracking refs of origin and prunes deleted ones.
func (g Git) Fetch(ctx context.Context, repoDir string) error {
	_, err := g.Run(ctx, repoDir, "fetch", "--prune", "--quiet", "origin")
	return err
}

// DefaultBranch returns the branch origin/HEAD points to, without the
// "origin/" prefix. It returns ErrNoDefaultBranch.
func (g Git) DefaultBranch(ctx context.Context, repoDir string) (string, error) {
	resolve := func() (string, error) {
		out, err := g.Run(ctx, repoDir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
		return strings.TrimPrefix(out, "origin/"), err
	}
	if b, err := resolve(); err == nil {
		return b, nil
	}
	if _, err := g.Run(ctx, repoDir, "remote", "set-head", "origin", "--auto"); err == nil {
		if b, err := resolve(); err == nil {
			return b, nil
		}
	}
	for _, b := range []string{"main", "master"} {
		if g.refExists(ctx, repoDir, "refs/remotes/origin/"+b) {
			return b, nil
		}
	}
	return "", ErrNoDefaultBranch
}

func (g Git) refExists(ctx context.Context, repoDir, ref string) bool {
	_, err := g.Run(ctx, repoDir, "show-ref", "--verify", "--quiet", ref)
	return err == nil
}

// LocalBranchExists reports whether refs/heads/branch exists.
func (g Git) LocalBranchExists(ctx context.Context, repoDir, branch string) bool {
	return g.refExists(ctx, repoDir, "refs/heads/"+branch)
}

// RemoteBranchExists reports whether refs/remotes/origin/branch exists.
func (g Git) RemoteBranchExists(ctx context.Context, repoDir, branch string) bool {
	return g.refExists(ctx, repoDir, "refs/remotes/origin/"+branch)
}

// CloneSession creates an independent repository at dir, checked out on
// branch, so the same branch can be used by any number of sessions. Objects
// are hardlinked from cache (copied across filesystems) and origin is set to
// url, then fetched. branch tracks origin/branch when it exists, otherwise it
// is created from origin/<default branch of cache>. A failed fetch is not
// fatal: the cache's refs are used and the returned error wraps ErrFetch.
func (g Git) CloneSession(ctx context.Context, cache, url, dir, branch string) error {
	def, err := g.DefaultBranch(ctx, cache)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if _, err := g.Run(ctx, filepath.Dir(dir), "clone", "--quiet", "--no-checkout", cache, dir); err != nil {
		return err
	}
	initial := g.CurrentBranch(ctx, dir)
	if _, err := g.Run(ctx, dir, "remote", "set-url", "origin", url); err != nil {
		return err
	}
	fetchErr := g.Fetch(ctx, dir)
	if _, err := g.Run(ctx, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+def); err != nil {
		return err
	}
	if g.RemoteBranchExists(ctx, dir, branch) {
		_, err = g.Run(ctx, dir, "checkout", "--quiet", "--track", "-B", branch, "origin/"+branch)
	} else {
		_, err = g.Run(ctx, dir, "checkout", "--quiet", "--no-track", "-B", branch, "origin/"+def)
	}
	if err != nil {
		return err
	}
	if initial != "" && initial != branch {
		if _, err := g.Run(ctx, dir, "branch", "-D", initial); err != nil {
			return err
		}
	}
	if fetchErr != nil {
		return errors.Join(ErrFetch, fetchErr)
	}
	return nil
}

// RemoteURL returns the URL of origin as configured, before url.*.insteadOf
// rewrites, so it can be compared with the URL aw cloned from.
func (g Git) RemoteURL(ctx context.Context, dir string) (string, error) {
	return g.Run(ctx, dir, "config", "--get", "remote.origin.url")
}

// CurrentBranch returns the checked-out branch, "" when HEAD is detached.
func (g Git) CurrentBranch(ctx context.Context, dir string) string {
	b, err := g.Run(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return b
}

// Status inspects the repository at dir. fallbackBase is compared against when
// the branch has no upstream (typically "origin/main").
func (g Git) Status(ctx context.Context, dir, fallbackBase string) (Status, error) {
	s := Status{Branch: g.CurrentBranch(ctx, dir)}
	porcelain, err := g.Run(ctx, dir, "status", "--porcelain")
	if err != nil {
		return s, err
	}
	if porcelain != "" {
		s.Dirty = strings.Count(porcelain, "\n") + 1
	}
	if up, err := g.Run(ctx, dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil {
		s.Upstream = up
		s.Base = up
	} else {
		s.Base = fallbackBase
	}
	if s.Base != "" {
		counts, err := g.Run(ctx, dir, "rev-list", "--left-right", "--count", "HEAD..."+s.Base)
		if err != nil {
			return s, err
		}
		left, right, _ := strings.Cut(counts, "\t")
		s.Ahead, _ = strconv.Atoi(left)
		s.Behind, _ = strconv.Atoi(right)
	}
	unpushed, err := g.Run(ctx, dir, "rev-list", "--count", "HEAD", "--branches", "--not", "--remotes")
	if err != nil {
		return s, err
	}
	s.Unpushed, _ = strconv.Atoi(unpushed)
	stashes, err := g.Run(ctx, dir, "rev-list", "--walk-reflogs", "--count", "refs/stash")
	if err == nil {
		s.Stashes, _ = strconv.Atoi(stashes)
	}
	return s, nil
}

// FastForward merges ref into the current branch only if no merge commit is
// needed.
func (g Git) FastForward(ctx context.Context, dir, ref string) error {
	_, err := g.Run(ctx, dir, "merge", "--ff-only", "--quiet", ref)
	return err
}
