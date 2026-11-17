// Package cli implements the aw command line.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/JLugagne/aw/internal/config"
	"github.com/JLugagne/aw/internal/fuzzy"
	"github.com/JLugagne/aw/internal/git"
	"github.com/JLugagne/aw/internal/workspace"
)

// ErrSessionNotFound is returned when a session id matches nothing, possibly
// wrapped with "did you mean" suggestions.
var ErrSessionNotFound = errors.New("session not found")

// ErrAborted is returned when the user declines a confirmation.
var ErrAborted = errors.New("aborted")

// App holds the I/O of one invocation; tests replace every field.
type App struct {
	Version string
	In      io.Reader
	Out     io.Writer
	Err     io.Writer
	// Interactive enables prompts. When false every confirmation is answered
	// "no", so scripts never delete or pull without explicit flags.
	Interactive bool
	// Exec replaces the current process with path; dir is its working
	// directory.
	Exec func(path string, argv []string, dir string, env []string) error

	configPath string
	reader     *bufio.Reader
}

// Execute runs aw with os.Args and returns the process exit code.
func Execute(version string) int {
	a := &App{
		Version:     version,
		In:          os.Stdin,
		Out:         os.Stdout,
		Err:         os.Stderr,
		Interactive: isTerminal(os.Stdin),
		Exec:        execReplace,
	}
	if err := a.Root().ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(a.Err, "aw:", err)
		return 1
	}
	return 0
}

func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// Root builds the command tree.
func (a *App) Root() *cobra.Command {
	root := &cobra.Command{
		Use:   "aw",
		Short: "Manage multi-repository agent workspaces",
		Long: `aw groups repositories into named sessions. Each session gets its own
directory with one git worktree per repository, backed by shared clones under
the base directory, and opens your coding agent (claude, opencode, ...) or
editor on it.

LLM agents and scripts: read "aw help agents" first.`,
		Version:       a.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetIn(a.In)
	root.SetOut(a.Out)
	root.SetErr(a.Err)
	root.PersistentFlags().StringVar(&a.configPath, "config", "", "config file (default $AW_CONFIG or ~/.config/aw/config.yaml)")
	root.AddCommand(
		a.lsCmd(),
		a.showCmd(),
		a.createCmd(),
		a.addCmd(),
		a.detachCmd(),
		a.removeCmd(),
		a.runCmd(),
		a.editCmd(),
		a.pathCmd(),
		a.configCmd(),
		a.syncCmd(),
		a.upgradeCmd(),
		a.agentsTopic(),
	)
	return root
}

func (a *App) load() (*config.Config, string, error) {
	path := a.configPath
	if path == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return nil, "", err
		}
		path = p
	}
	cfg, err := config.Load(path)
	return cfg, path, err
}

func (a *App) manager(cfg *config.Config) *workspace.Manager {
	return &workspace.Manager{Config: cfg, Git: git.Git{Progress: a.Err}, Log: a.Err}
}

func (a *App) confirm(question string, def bool) bool {
	if !a.Interactive {
		return false
	}
	if a.reader == nil {
		a.reader = bufio.NewReader(a.In)
	}
	hint := " [y/N] "
	if def {
		hint = " [Y/n] "
	}
	fmt.Fprint(a.Err, question+hint)
	line, err := a.reader.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(a.Err)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "o", "oui":
		return true
	case "":
		return def
	}
	return false
}

func (a *App) aborted() error {
	if a.Interactive {
		return ErrAborted
	}
	return errors.Join(ErrAborted, errors.New("confirmation needed but no terminal: pass -y when nothing would be lost, --force to discard local work"))
}

type notFoundError struct {
	id          string
	suggestions []string
}

func (e notFoundError) Error() string {
	msg := fmt.Sprintf("session %q not found", e.id)
	if len(e.suggestions) > 0 {
		msg += "; did you mean: " + strings.Join(e.suggestions, ", ") + "?"
	}
	return msg
}

func (e notFoundError) Is(target error) bool {
	return target == ErrSessionNotFound
}

func (a *App) resolve(cfg *config.Config, id string, guess bool) (string, *config.Session, error) {
	if s, ok := cfg.Sessions[id]; ok {
		return id, s, nil
	}
	candidates := make([]fuzzy.Candidate, 0, len(cfg.Sessions))
	for _, sid := range sortedIDs(cfg) {
		candidates = append(candidates, fuzzy.Candidate{ID: sid, Name: cfg.Sessions[sid].Name})
	}
	sugg := fuzzy.Suggest(id, candidates, 3)
	if guess && len(sugg) > 0 && a.confirm(fmt.Sprintf("session %q not found. Did you mean %q?", id, sugg[0]), true) {
		return sugg[0], cfg.Sessions[sugg[0]], nil
	}
	return "", nil, notFoundError{id: id, suggestions: sugg}
}

func sortedIDs(cfg *config.Config) []string {
	ids := make([]string, 0, len(cfg.Sessions))
	for id := range cfg.Sessions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (a *App) completeSessions(_ *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, _, err := a.load()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var out []cobra.Completion
	for _, id := range sortedIDs(cfg) {
		out = append(out, cobra.CompletionWithDesc(id, cfg.Sessions[id].Name))
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
