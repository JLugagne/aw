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

// ErrNoDefaultBranch is returned by DefaultBranch when origin/HEAD cannot be
// resolved and neither origin/main nor origin/master exists.
var ErrNoDefaultBranch = errors.New("cannot determine the default branch of origin")

// Git runs git commands. The zero value is usable.
type Git struct {
	// Progress receives the output of clone; nil discards it.
	Progress io.Writer
}

// Status describes a worktree relative to the ref it should track.
type Status struct {
	Branch string `json:"branch"`
	// Upstream is the tracking ref, empty when the branch has none.
	Upstream string `json:"upstream,omitempty"`
	// Base is the ref Ahead and Behind are computed against: Upstream when
	// set, otherwise the remote default branch.
	Base     string `json:"base,omitempty"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
	Dirty    int    `json:"dirty"`
	Unpushed int    `json:"unpushed"`
}

// CanFastForward reports whether merging Base only moves HEAD forward.
func (s Status) CanFastForward() bool {
	return s.Behind > 0 && s.Ahead == 0
}

// HasLocalWork reports changes that are lost when the worktree is deleted.
// Unpushed counts commits absent from every remote-tracking ref.
func (s Status) HasLocalWork() bool {
	return s.Dirty > 0 || s.Unpushed > 0
}

func (g Git) run(ctx context.Context, dir string, args ...string) (string, error) {
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
	_, err := g.run(ctx, repoDir, "fetch", "--prune", "--quiet", "origin")
	return err
}

// DefaultBranch returns the branch origin/HEAD points to, without the
// "origin/" prefix. It returns ErrNoDefaultBranch.
func (g Git) DefaultBranch(ctx context.Context, repoDir string) (string, error) {
	resolve := func() (string, error) {
		out, err := g.run(ctx, repoDir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
		return strings.TrimPrefix(out, "origin/"), err
	}
	if b, err := resolve(); err == nil {
		return b, nil
	}
	if _, err := g.run(ctx, repoDir, "remote", "set-head", "origin", "--auto"); err == nil {
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
	_, err := g.run(ctx, repoDir, "show-ref", "--verify", "--quiet", ref)
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

// AddWorktree checks branch out in dir. An existing local branch is used as
// is; otherwise branch is created from origin/branch (tracking it) when that
// exists, or from origin/<defaultBranch> without tracking. Git refuses when
// the branch is already checked out in another worktree.
func (g Git) AddWorktree(ctx context.Context, repoDir, dir, branch string) error {
	if _, err := g.run(ctx, repoDir, "worktree", "prune"); err != nil {
		return err
	}
	if g.LocalBranchExists(ctx, repoDir, branch) {
		_, err := g.run(ctx, repoDir, "worktree", "add", dir, branch)
		return err
	}
	if g.RemoteBranchExists(ctx, repoDir, branch) {
		_, err := g.run(ctx, repoDir, "worktree", "add", "--track", "-b", branch, dir, "origin/"+branch)
		return err
	}
	def, err := g.DefaultBranch(ctx, repoDir)
	if err != nil {
		return err
	}
	_, err = g.run(ctx, repoDir, "worktree", "add", "--no-track", "-b", branch, dir, "origin/"+def)
	return err
}

// RemoveWorktree deletes the worktree at dir, discarding uncommitted changes.
func (g Git) RemoveWorktree(ctx context.Context, repoDir, dir string) error {
	_, err := g.run(ctx, repoDir, "worktree", "remove", "--force", "--force", dir)
	if err != nil {
		if _, statErr := os.Stat(dir); errors.Is(statErr, os.ErrNotExist) {
			_, err = g.run(ctx, repoDir, "worktree", "prune")
		}
	}
	return err
}

// DeleteBranch force-deletes a local branch.
func (g Git) DeleteBranch(ctx context.Context, repoDir, branch string) error {
	_, err := g.run(ctx, repoDir, "branch", "-D", branch)
	return err
}

// WorktreeInfo returns the main repository a linked worktree belongs to and
// its checked-out branch ("" when HEAD is detached).
func (g Git) WorktreeInfo(ctx context.Context, dir string) (string, string, error) {
	out, err := g.run(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", "", err
	}
	common, branch, _ := strings.Cut(out, "\n")
	repoDir := common
	if filepath.Base(common) == ".git" {
		repoDir = filepath.Dir(common)
	}
	if branch == "HEAD" {
		branch = ""
	}
	return repoDir, branch, nil
}

// Status inspects the worktree at dir. fallbackBase is compared against when
// the branch has no upstream (typically "origin/main").
func (g Git) Status(ctx context.Context, dir, fallbackBase string) (Status, error) {
	var s Status
	branch, err := g.run(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err == nil {
		s.Branch = branch
	}
	porcelain, err := g.run(ctx, dir, "status", "--porcelain")
	if err != nil {
		return s, err
	}
	if porcelain != "" {
		s.Dirty = strings.Count(porcelain, "\n") + 1
	}
	if up, err := g.run(ctx, dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil {
		s.Upstream = up
		s.Base = up
	} else {
		s.Base = fallbackBase
	}
	if s.Base != "" {
		counts, err := g.run(ctx, dir, "rev-list", "--left-right", "--count", "HEAD..."+s.Base)
		if err != nil {
			return s, err
		}
		left, right, _ := strings.Cut(counts, "\t")
		s.Ahead, _ = strconv.Atoi(left)
		s.Behind, _ = strconv.Atoi(right)
	}
	unpushed, err := g.run(ctx, dir, "rev-list", "--count", "HEAD", "--not", "--remotes")
	if err != nil {
		return s, err
	}
	s.Unpushed, _ = strconv.Atoi(unpushed)
	return s, nil
}

// FastForward merges ref into the current branch only if no merge commit is
// needed.
func (g Git) FastForward(ctx context.Context, dir, ref string) error {
	_, err := g.run(ctx, dir, "merge", "--ff-only", "--quiet", ref)
	return err
}
