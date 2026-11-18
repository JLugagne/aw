package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/JLugagne/aw/internal/config"
	"github.com/JLugagne/aw/internal/configsync"
	"github.com/JLugagne/aw/internal/git"
)

// ErrDesync is returned when the local and remote configurations both changed
// the same unit and no resolution was given.
var ErrDesync = errors.New("config desync")

const (
	syncAnnotation = "aw-sync"
	syncMutate     = "mutate"
	syncPull       = "pull"
)

func (a *App) configCmd() *cobra.Command {
	var printPath, local bool
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Edit the config file and sync it between machines",
		Long: `Without a subcommand, open the config file in your editor (--local: the
machine-specific overlay config.local.yaml, never synced).

config.yaml can be shared between machines through any git remote: a GitHub
gist, a GitLab snippet or a repository ("aw config remote"). Each push is a
revision; a machine only pushes on top of the revision it last saw, otherwise
changes are merged session by session and you are asked only when the same
session (or the global settings) changed on both sides.`,
		Example: `  aw config
  aw config --local
  aw config remote git@gitlab.com:snippets/4242.git --auto
  aw config status
  aw config sync`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, path, err := a.load()
			if err != nil {
				return err
			}
			if local {
				path = cfg.LocalPath()
			}
			if printPath {
				fmt.Fprintln(a.Out, path)
				return nil
			}
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				data := []byte("# machine-specific settings overriding config.yaml (never synced)\n")
				if !local {
					if data, err = cfg.Marshal(); err != nil {
						return err
					}
				}
				if err := config.WriteFile(path, data); err != nil {
					return err
				}
			}
			return a.launch(cfg.EditorCommand(""), []string{path}, filepath.Dir(path), nil)
		},
	}
	cmd.Flags().BoolVar(&printPath, "path", false, "only print the file path")
	cmd.Flags().BoolVar(&local, "local", false, "use the machine-specific overlay config.local.yaml")
	cmd.AddCommand(a.configRemoteCmd(), a.configStatusCmd(), a.configSyncCmd())
	return cmd
}

func (a *App) configRemoteCmd() *cobra.Command {
	var file string
	var auto, unset bool
	cmd := &cobra.Command{
		Use:   "remote [git-url]",
		Short: "Show or set the git remote config.yaml is synced with",
		Long: `Show or set the remote, stored in config.local.yaml. Any git URL works:
  GitHub gist      git@gist.github.com:<id>.git
  GitLab snippet   git@gitlab.com:snippets/<id>.git
  repository       git@host:owner/name.git
A GitHub "secret" gist is readable by anyone with its URL; GitLab snippets
can be private.`,
		Example: `  aw config remote git@gist.github.com:ab12cd34.git --auto
  aw config remote --unset`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := a.load()
			if err != nil {
				return err
			}
			s := cfg.SyncSettings()
			switch {
			case unset:
				if err := cfg.SetSync(config.Sync{}); err != nil {
					return err
				}
				fmt.Fprintln(a.Out, "config sync disabled")
				return nil
			case len(args) == 0 && !cmd.Flags().Changed("file") && !cmd.Flags().Changed("auto"):
				if s.Remote == "" {
					fmt.Fprintln(a.Out, "not configured: aw config remote <git url>")
					return nil
				}
				fmt.Fprintf(a.Out, "remote: %s\nfile:   %s\nauto:   %t\n", git.Redact(s.Remote), s.FileName(), s.Auto)
				return nil
			}
			if len(args) == 1 {
				s.Remote = args[0]
			}
			if cmd.Flags().Changed("file") {
				s.File = file
			}
			if cmd.Flags().Changed("auto") {
				s.Auto = auto
			}
			if s.Remote == "" {
				return errors.New("a git URL is required")
			}
			if strings.HasPrefix(s.Remote, "-") {
				return fmt.Errorf("invalid git URL %q", s.Remote)
			}
			if err := s.Validate(); err != nil {
				return err
			}
			if err := cfg.SetSync(s); err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "config synced with %s (%s, auto=%t) - run: aw config sync\n", git.Redact(s.Remote), s.FileName(), s.Auto)
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "file name inside the remote (default "+config.DefaultSyncFile+")")
	cmd.Flags().BoolVar(&auto, "auto", false, "sync automatically around commands that read or modify the config")
	cmd.Flags().BoolVar(&unset, "unset", false, "disable config sync")
	return cmd
}

func (a *App) syncer(cfg *config.Config, path string) (*configsync.Syncer, error) {
	dir, err := configsync.StateDirFor(path)
	if err != nil {
		return nil, err
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return &configsync.Syncer{
		Git:        git.Git{},
		StateDir:   dir,
		Settings:   cfg.SyncSettings(),
		ConfigPath: path,
		Host:       host,
		Log:        a.Err,
	}, nil
}

func (a *App) configStatusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Compare the local config with the remote revision",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, path, err := a.load()
			if err != nil {
				return err
			}
			s, err := a.syncer(cfg, path)
			if err != nil {
				return err
			}
			st, err := s.Status(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(a.Out)
				enc.SetIndent("", "  ")
				return enc.Encode(st)
			}
			fmt.Fprintf(a.Out, "remote:   %s\nlocal:    based on %s", git.Redact(s.Settings.Remote), describeRevision(st.Base))
			if st.LocalChanged {
				fmt.Fprint(a.Out, ", modified since")
			}
			fmt.Fprintf(a.Out, "\nupstream: %s\n", describeRevision(st.Remote))
			switch {
			case st.InSync():
				fmt.Fprintln(a.Out, "in sync")
			case st.LocalChanged && st.RemoteChanged:
				fmt.Fprintln(a.Out, "DESYNC: both sides changed - aw config sync merges them")
			case st.LocalChanged:
				fmt.Fprintln(a.Out, "local changes to push - aw config sync")
			default:
				fmt.Fprintln(a.Out, "remote changes to pull - aw config sync")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the status as JSON")
	return cmd
}

func (a *App) configSyncCmd() *cobra.Command {
	var ours, theirs, asJSON bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Pull, push or merge config.yaml with the remote",
		Long: `Bring config.yaml and the remote to the same revision: pull when only the
remote changed, push when only this machine changed, otherwise merge session
by session. When the same session (or the global settings) changed on both
sides you are shown the diff and asked which side to keep; without a
terminal pass --ours or --theirs.`,
		Example: `  aw config sync
  aw config sync --theirs`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, path, err := a.load()
			if err != nil {
				return err
			}
			s, err := a.syncer(cfg, path)
			if err != nil {
				return err
			}
			choice := choiceNone
			switch {
			case ours:
				choice = choiceOurs
			case theirs:
				choice = choiceTheirs
			}
			res, err := s.Sync(cmd.Context(), "aw config sync", a.resolver(choice))
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(a.Out)
				enc.SetIndent("", "  ")
				return enc.Encode(res)
			}
			a.printSyncResult(res, true)
			return nil
		},
	}
	cmd.Flags().BoolVar(&ours, "ours", false, "on conflict keep the local version")
	cmd.Flags().BoolVar(&theirs, "theirs", false, "on conflict take the remote version")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON")
	cmd.MarkFlagsMutuallyExclusive("ours", "theirs")
	return cmd
}

type resolution int

const (
	choiceNone resolution = iota
	choiceOurs
	choiceTheirs
)

func (a *App) resolver(choice resolution) configsync.Resolver {
	return func(c configsync.Conflict) (configsync.Choice, error) {
		switch choice {
		case choiceOurs:
			return configsync.KeepLocal, nil
		case choiceTheirs:
			return configsync.TakeRemote, nil
		}
		if !a.Interactive {
			return 0, errors.Join(ErrDesync, fmt.Errorf("%s changed on both sides; run: aw config sync --ours (keep local) or --theirs (take remote)", c.Key))
		}
		fmt.Fprintf(a.Err, "\nDESYNC on %s (- local, + remote):\n%s", c.Key, configsync.Diff(c.Local, c.Remote))
		for {
			fmt.Fprint(a.Err, "Keep [l]ocal, take [r]emote or [a]bort? ")
			switch strings.ToLower(strings.TrimSpace(a.readLine())) {
			case "l", "local":
				return configsync.KeepLocal, nil
			case "r", "remote":
				return configsync.TakeRemote, nil
			case "a", "abort", "":
				return 0, errors.Join(ErrDesync, ErrAborted)
			}
		}
	}
}

func (a *App) printSyncResult(res configsync.Result, verbose bool) {
	for _, line := range res.Report {
		fmt.Fprintf(a.Err, "  %s\n", line)
	}
	switch {
	case res.Pushed && res.Pulled:
		fmt.Fprintf(a.Err, "config merged and pushed as %s\n", describeRevision(res.Revision))
	case res.Pushed:
		fmt.Fprintf(a.Err, "config pushed as %s\n", describeRevision(res.Revision))
	case res.Pulled:
		fmt.Fprintf(a.Err, "config updated to %s\n", describeRevision(res.Revision))
	case verbose:
		fmt.Fprintf(a.Err, "config in sync at %s\n", describeRevision(res.Revision))
	}
}

func describeRevision(r configsync.Revision) string {
	if r.SHA == "" {
		return "nothing yet"
	}
	out := fmt.Sprintf("revision %d", r.Number)
	var details []string
	if r.Host != "" {
		details = append(details, r.Host)
	}
	if !r.Time.IsZero() {
		details = append(details, since(time.Since(r.Time)))
	}
	if len(details) > 0 {
		out += " (" + strings.Join(details, ", ") + ")"
	}
	return out
}

func since(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func (a *App) autoSync(cmd *cobra.Command, args []string, after bool) error {
	mode := cmd.Annotations[syncAnnotation]
	if mode == "" || (after && mode != syncMutate) {
		return nil
	}
	cfg, path, err := a.load()
	if err != nil {
		return err
	}
	if s := cfg.SyncSettings(); !s.Auto || s.Remote == "" {
		return nil
	}
	s, err := a.syncer(cfg, path)
	if err != nil {
		return err
	}
	s.Log = nil
	message := strings.TrimSpace(strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()) + " " + strings.Join(args, " "))
	res, err := s.Sync(cmd.Context(), message, a.resolver(choiceNone))
	switch {
	case errors.Is(err, ErrDesync):
		return err
	case err != nil && after:
		fmt.Fprintf(a.Err, "warning: config not pushed (%v); run: aw config sync\n", errorLine(err))
		return nil
	case err != nil:
		fmt.Fprintf(a.Err, "warning: config sync skipped (%v)\n", errorLine(err))
		return nil
	}
	a.printSyncResult(res, false)
	return nil
}

func errorLine(err error) string {
	lines := strings.Split(strings.TrimSpace(err.Error()), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && !strings.HasPrefix(l, "exit status") {
			return l
		}
	}
	return err.Error()
}
