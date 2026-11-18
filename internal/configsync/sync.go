// Package configsync shares the aw configuration between machines through any
// git remote: a GitHub gist, a GitLab snippet or a repository. Every push is a
// commit whose position in the history is the revision number. Like an event
// stream, a machine only pushes on top of the revision it last synced; when
// both sides moved, the changes are merged session by session and only units
// modified on both sides are handed to a Resolver.
package configsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/JLugagne/aw/internal/config"
	"github.com/JLugagne/aw/internal/git"
)

var (
	// ErrNotConfigured is returned when no remote is set.
	ErrNotConfigured = errors.New("config sync is not configured: aw config remote <git url>")
	// ErrInvalidRemote is returned when the remote file is not a valid config.
	ErrInvalidRemote = errors.New("the remote config is invalid")
)

const (
	maxAttempts   = 3
	subjectPrefix = "aw@"
)

// Revision identifies a pushed state of the configuration.
type Revision struct {
	SHA    string    `json:"sha,omitempty"`
	Number int       `json:"number"`
	Host   string    `json:"host,omitempty"`
	Time   time.Time `json:"time"`
}

// Status compares this machine with the remote.
type Status struct {
	// Base is the revision this machine last synced, zero if never.
	Base Revision `json:"base"`
	// Remote is the current remote revision, zero if the remote is empty.
	Remote        Revision `json:"remote"`
	LocalChanged  bool     `json:"local_changed"`
	RemoteChanged bool     `json:"remote_changed"`
}

// InSync reports that neither side changed since the last sync.
func (s Status) InSync() bool {
	return !s.LocalChanged && !s.RemoteChanged
}

// Result describes what Sync did.
type Result struct {
	// Status is the state before syncing.
	Status   Status   `json:"status"`
	Pulled   bool     `json:"pulled"`
	Pushed   bool     `json:"pushed"`
	Revision Revision `json:"revision"`
	// Report lists the units merged when both sides had changed.
	Report []string `json:"report,omitempty"`
}

// Syncer syncs one configuration file. Its commits are bookkeeping: they are
// never signed and skip commit hooks. It is not safe for concurrent use.
type Syncer struct {
	Git git.Git
	// StateDir holds the clone of the remote and the last synced revision;
	// see StateDirFor.
	StateDir   string
	Settings   config.Sync
	ConfigPath string
	// Host is recorded in commit messages to tell machines apart.
	Host string
	Log  io.Writer
}

type state struct {
	Remote string   `json:"remote"`
	File   string   `json:"file"`
	Base   Revision `json:"base"`
	Hash   string   `json:"hash"`
}

// StateDirFor returns the state directory of configPath under
// $XDG_STATE_HOME/aw/sync (default ~/.local/state/aw/sync).
func StateDirFor(configPath string) (string, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".local", "state")
	}
	abs, err := filepath.Abs(configPath)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(root, "aw", "sync", hex.EncodeToString(sum[:6])), nil
}

func (s *Syncer) repoDir() string {
	return filepath.Join(s.StateDir, "repo")
}

func (s *Syncer) logf(format string, args ...any) {
	if s.Log != nil {
		fmt.Fprintf(s.Log, format, args...)
	}
}

func (s *Syncer) prepare(ctx context.Context) (string, error) {
	if s.Settings.Remote == "" {
		return "", ErrNotConfigured
	}
	if err := s.Settings.Validate(); err != nil {
		return "", err
	}
	dir := s.repoDir()
	if url, err := s.Git.RemoteURL(ctx, dir); err != nil || url != s.Settings.Remote {
		if err := os.RemoveAll(dir); err != nil {
			return "", err
		}
		if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
			return "", err
		}
		s.logf("cloning %s\n", git.Redact(s.Settings.Remote))
		if _, err := s.Git.Run(ctx, s.StateDir, "clone", "--quiet", "--", s.Settings.Remote, dir); err != nil {
			return "", err
		}
	} else if err := s.Git.Fetch(ctx, dir); err != nil {
		return "", err
	}
	return s.Git.Run(ctx, dir, "symbolic-ref", "--short", "HEAD")
}

func (s *Syncer) remoteSHA(ctx context.Context, branch string) string {
	sha, err := s.Git.Run(ctx, s.repoDir(), "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	if err != nil {
		return ""
	}
	return sha
}

func (s *Syncer) content(ctx context.Context, sha string) []byte {
	if sha == "" {
		return nil
	}
	out, err := s.Git.Run(ctx, s.repoDir(), "show", sha+":"+s.Settings.FileName())
	if err != nil {
		return nil
	}
	return []byte(out + "\n")
}

func (s *Syncer) revision(ctx context.Context, sha string) Revision {
	if sha == "" {
		return Revision{}
	}
	r := Revision{SHA: sha}
	if n, err := s.Git.Run(ctx, s.repoDir(), "rev-list", "--count", sha); err == nil {
		r.Number, _ = strconv.Atoi(n)
	}
	if out, err := s.Git.Run(ctx, s.repoDir(), "log", "-1", "--format=%ct%x00%s", sha); err == nil {
		ts, subject, _ := strings.Cut(out, "\x00")
		if sec, err := strconv.ParseInt(ts, 10, 64); err == nil {
			r.Time = time.Unix(sec, 0)
		}
		if rest, ok := strings.CutPrefix(subject, subjectPrefix); ok {
			r.Host, _, _ = strings.Cut(rest, ":")
		}
	}
	return r
}

func (s *Syncer) loadState() state {
	var st state
	data, err := os.ReadFile(filepath.Join(s.StateDir, "state.json"))
	if err != nil || json.Unmarshal(data, &st) != nil {
		return state{}
	}
	if st.Remote != s.Settings.Remote || st.File != s.Settings.FileName() {
		return state{}
	}
	return st
}

func (s *Syncer) saveState(ctx context.Context, sha string, data []byte) (Revision, error) {
	rev := s.revision(ctx, sha)
	st := state{Remote: s.Settings.Remote, File: s.Settings.FileName(), Base: rev, Hash: hash(data)}
	out, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return rev, err
	}
	return rev, config.WriteFile(filepath.Join(s.StateDir, "state.json"), out)
}

func hash(data []byte) string {
	if data == nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *Syncer) readLocal() ([]byte, error) {
	data, err := os.ReadFile(s.ConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

type snapshot struct {
	branch, sha   string
	local, remote []byte
	state         state
	status        Status
}

func (s *Syncer) snapshot(ctx context.Context) (snapshot, error) {
	branch, err := s.prepare(ctx)
	if err != nil {
		return snapshot{}, err
	}
	local, err := s.readLocal()
	if err != nil {
		return snapshot{}, err
	}
	sn := snapshot{branch: branch, sha: s.remoteSHA(ctx, branch), local: local, state: s.loadState()}
	sn.remote = s.content(ctx, sn.sha)
	sn.status = Status{
		Base:          sn.state.Base,
		Remote:        s.revision(ctx, sn.sha),
		LocalChanged:  hash(local) != sn.state.Hash,
		RemoteChanged: sn.sha != sn.state.Base.SHA,
	}
	return sn, nil
}

// Status fetches the remote and compares it with this machine without
// changing anything. It returns ErrNotConfigured.
func (s *Syncer) Status(ctx context.Context) (Status, error) {
	sn, err := s.snapshot(ctx)
	return sn.status, err
}

// Sync pulls, pushes or merges so that both sides hold the same
// configuration. On the first sync of a machine the remote global settings
// win and sessions are merged. message describes the change in the commit. resolve is
// called for every unit modified on both sides; its error aborts the sync
// without touching the local file or the remote. It returns ErrNotConfigured
// and ErrInvalidRemote.
func (s *Syncer) Sync(ctx context.Context, message string, resolve Resolver) (Result, error) {
	var first *Status
	for attempt := 1; ; attempt++ {
		sn, err := s.snapshot(ctx)
		if err != nil {
			return Result{}, err
		}
		if first == nil {
			first = &sn.status
		}
		res, retry, err := s.apply(ctx, sn, message, resolve)
		res.Status = *first
		if !retry || attempt == maxAttempts {
			return res, err
		}
		s.logf("remote changed during sync, retrying\n")
	}
}

func (s *Syncer) apply(ctx context.Context, sn snapshot, message string, resolve Resolver) (Result, bool, error) {
	var res Result
	st := sn.status
	switch {
	case st.InSync():
		res.Revision = st.Remote
		return res, false, nil
	case bytes.Equal(sn.local, sn.remote) && sn.remote != nil:
		rev, err := s.saveState(ctx, sn.sha, sn.remote)
		res.Revision = rev
		return res, false, err
	case !st.LocalChanged && sn.remote != nil:
		if _, err := config.Parse(sn.remote); err != nil {
			return res, false, errors.Join(ErrInvalidRemote, err)
		}
		if err := config.WriteFile(s.ConfigPath, sn.remote); err != nil {
			return res, false, err
		}
		rev, err := s.saveState(ctx, sn.sha, sn.remote)
		res.Pulled, res.Revision = true, rev
		return res, false, err
	case !st.RemoteChanged || sn.remote == nil:
		if sn.local == nil {
			res.Revision = st.Remote
			return res, false, nil
		}
		return s.push(ctx, sn, sn.local, message, res)
	}
	localCfg, err := config.Parse(sn.local)
	if err != nil {
		return res, false, err
	}
	baseCfg, err := config.Parse(s.content(ctx, sn.state.Base.SHA))
	if err != nil || sn.state.Base.SHA == "" {
		baseCfg = settingsOf(localCfg)
		baseCfg.Sessions = map[string]*config.Session{}
	}
	remoteCfg, err := config.Parse(sn.remote)
	if err != nil {
		return res, false, errors.Join(ErrInvalidRemote, err)
	}
	merged, report, err := Merge(baseCfg, localCfg, remoteCfg, resolve)
	if err != nil {
		return res, false, err
	}
	data, err := merged.Marshal()
	if err != nil {
		return res, false, err
	}
	res.Report = report
	if bytes.Equal(data, sn.remote) {
		if err := config.WriteFile(s.ConfigPath, sn.remote); err != nil {
			return res, false, err
		}
		rev, err := s.saveState(ctx, sn.sha, sn.remote)
		res.Pulled, res.Revision = true, rev
		return res, false, err
	}
	res, retry, err := s.push(ctx, sn, data, message, res)
	if err == nil && !retry {
		res.Pulled = true
		err = config.WriteFile(s.ConfigPath, data)
	}
	return res, retry, err
}

func (s *Syncer) push(ctx context.Context, sn snapshot, data []byte, message string, res Result) (Result, bool, error) {
	dir := s.repoDir()
	if sn.sha != "" {
		if _, err := s.Git.Run(ctx, dir, "reset", "--quiet", "--hard", sn.sha); err != nil {
			return res, false, err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, s.Settings.FileName()), data, 0o600); err != nil {
		return res, false, err
	}
	if _, err := s.Git.Run(ctx, dir, "add", "--", s.Settings.FileName()); err != nil {
		return res, false, err
	}
	commit := []string{"-c", "commit.gpgsign=false", "commit", "--quiet", "--no-verify", "-m", subjectPrefix + s.Host + ": " + message}
	if email, err := s.Git.Run(ctx, dir, "config", "user.email"); err != nil || email == "" {
		commit = append([]string{"-c", "user.name=aw", "-c", "user.email=aw@" + s.Host}, commit...)
	}
	if _, err := s.Git.Run(ctx, dir, commit...); err != nil {
		return res, false, err
	}
	if _, err := s.Git.Run(ctx, dir, "push", "--quiet", "origin", "HEAD:refs/heads/"+sn.branch); err != nil {
		if fetchErr := s.Git.Fetch(ctx, dir); fetchErr == nil && s.remoteSHA(ctx, sn.branch) != sn.sha {
			return res, true, err
		}
		return res, false, err
	}
	sha, err := s.Git.Run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return res, false, err
	}
	rev, err := s.saveState(ctx, sha, data)
	res.Pushed, res.Revision = true, rev
	return res, false, err
}
