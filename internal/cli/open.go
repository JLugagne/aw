package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/JLugagne/aw/internal/config"
	"github.com/JLugagne/aw/internal/workspace"
)

type openOptions struct {
	noFetch bool
	pull    bool
	noPull  bool
}

func (o *openOptions) register(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&o.noFetch, "no-fetch", false, "do not fetch remotes")
	cmd.Flags().BoolVar(&o.pull, "pull", false, "fast-forward repositories that are behind without asking")
	cmd.Flags().BoolVar(&o.noPull, "no-pull", false, "never pull")
	cmd.MarkFlagsMutuallyExclusive("pull", "no-pull")
}

func (a *App) runCmd() *cobra.Command {
	var opts openOptions
	var tool string
	var resume bool
	cmd := &cobra.Command{
		Use:   "run <session> [-- tool args...]",
		Short: "Prepare a session and launch the coding agent in it",
		Long: `Clone missing repositories, fetch the others, create missing worktrees,
report repositories that are behind and offer to fast-forward them, then
replace aw with the tool (default from the config, "claude" otherwise),
started in the session directory with --add-dir for every repository.
Arguments after -- are passed to the tool.

The repository directories are given on every launch, so resume through aw
("aw run -c <session>" or "aw run <session> -- --resume <id>") rather than
with the tool alone, which would not see them again. For claude,
CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1 is set (unless already in
your environment) so each repository's CLAUDE.md is loaded.

This command replaces aw with an interactive program: agents and scripts
should use "aw sync" instead.`,
		Example: `  aw run billing
  aw run --tool=opencode billing
  aw run -c billing                    # resume the latest conversation
  aw run billing -- --resume <id>      # resume a given conversation`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: a.completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := a.load()
			if err != nil {
				return err
			}
			id, dir, entries, err := a.open(cmd.Context(), cfg, args[0], opts)
			if err != nil {
				return err
			}
			if tool == "" {
				tool = cfg.ToolName()
			}
			t := cfg.ResolveTool(tool)
			argv := append([]string{t.Command}, t.Args...)
			if resume {
				if len(t.ContinueArgs) == 0 {
					return fmt.Errorf("tool %q has no continue_args in the config; pass its resume flag after --", tool)
				}
				argv = append(argv, t.ContinueArgs...)
			}
			if t.AddDirFlag != "" {
				for _, e := range entries {
					if e.Present() {
						argv = append(argv, t.AddDirFlag, e.Dir)
					}
				}
			}
			fmt.Fprintf(a.Err, "resume later with: aw run -c %s\n", id)
			return a.launch(argv, args[1:], dir, append([]string{"AW_SESSION=" + id}, defaultEnv(t.Env)...))
		},
	}
	opts.register(cmd)
	cmd.Flags().StringVarP(&tool, "tool", "t", "", "tool to launch (claude, opencode, codex or any command)")
	cmd.Flags().BoolVarP(&resume, "continue", "c", false, "resume the latest conversation of the session (tool continue_args)")
	return cmd
}

func (a *App) editCmd() *cobra.Command {
	var opts openOptions
	var editor string
	cmd := &cobra.Command{
		Use:   "edit <session>",
		Short: "Prepare a session and open it in your editor",
		Long: `Same preparation as "aw run", then open the session directory with the
editor from the config ("editor" key), $VISUAL or $EDITOR.`,
		Example: `  aw edit billing
  aw edit -e "code -n" billing`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: a.completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := a.load()
			if err != nil {
				return err
			}
			id, dir, _, err := a.open(cmd.Context(), cfg, args[0], opts)
			if err != nil {
				return err
			}
			return a.launch(cfg.EditorCommand(editor), []string{dir}, dir, []string{"AW_SESSION=" + id})
		},
	}
	opts.register(cmd)
	cmd.Flags().StringVarP(&editor, "editor", "e", "", "editor command overriding the config, e.g. \"code -n\"")
	return cmd
}

func (a *App) syncCmd() *cobra.Command {
	var opts openOptions
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "sync <session>",
		Short: "Prepare a session (clone, fetch, worktrees) without launching anything",
		Long: `Same preparation as "aw run" without launching a tool: clone missing
repositories, fetch, create missing worktrees, report the state and
fast-forward when --pull is given (or confirmed interactively). Safe for
agents and scripts. Exits non-zero if a repository could not be prepared.`,
		Example: `  aw sync billing
  aw sync --pull --json "$AW_SESSION"`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: a.completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := a.load()
			if err != nil {
				return err
			}
			id, dir, entries, err := a.open(cmd.Context(), cfg, args[0], opts)
			if err != nil {
				return err
			}
			if asJSON {
				return writeEntriesJSON(a.Out, id, cfg.Sessions[id], dir, entries)
			}
			fmt.Fprintln(a.Out, dir)
			return nil
		},
	}
	opts.register(cmd)
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the resulting state as JSON on stdout")
	return cmd
}

func (a *App) launch(argv, extra []string, dir string, env []string) error {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return errors.Join(fmt.Errorf("cannot launch %q", argv[0]), err)
	}
	full := append(append([]string{}, argv...), extra...)
	return a.Exec(path, full, dir, mergeEnv(os.Environ(), append([]string{"PWD=" + dir}, env...)))
}

func mergeEnv(base, overrides []string) []string {
	keys := map[string]bool{}
	for _, kv := range overrides {
		k, _, _ := strings.Cut(kv, "=")
		keys[k] = true
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		if k, _, _ := strings.Cut(kv, "="); !keys[k] {
			out = append(out, kv)
		}
	}
	return append(out, overrides...)
}

func defaultEnv(env map[string]string) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(env)) {
		if _, set := os.LookupEnv(k); !set {
			out = append(out, k+"="+env[k])
		}
	}
	return out
}

func (a *App) open(ctx context.Context, cfg *config.Config, query string, opts openOptions) (string, string, []workspace.Entry, error) {
	id, s, err := a.resolve(cfg, query, true)
	if err != nil {
		return "", "", nil, err
	}
	mgr := a.manager(cfg)
	dir, err := mgr.SessionDir(id)
	if err != nil {
		return "", "", nil, err
	}
	entries, err := mgr.Prepare(ctx, id, s, !opts.noFetch)
	if err != nil {
		return "", "", nil, err
	}
	printHeader(a.Err, id, s, dir)
	printEntries(a.Err, entries)

	if failed := countFailed(entries); failed > 0 && !a.confirm(fmt.Sprintf("%d repositories could not be prepared. Continue anyway?", failed), false) {
		return "", "", nil, errors.Join(a.aborted(), fmt.Errorf("%d repositories could not be prepared", failed))
	}

	var behind, diverged []string
	for _, e := range entries {
		switch {
		case e.Err != nil || !e.Present():
		case e.Status.CanFastForward():
			behind = append(behind, e.Ref.Path)
		case e.Status.Behind > 0:
			diverged = append(diverged, e.Ref.Path)
		}
	}
	for _, d := range diverged {
		fmt.Fprintf(a.Err, "note: %s has diverged from its base; merge or rebase it yourself\n", d)
	}
	if len(behind) > 0 && !opts.noPull {
		if opts.pull || a.confirm(fmt.Sprintf("%d repositories behind (%s). Pull (fast-forward)?", len(behind), strings.Join(behind, ", ")), true) {
			mgr.FastForward(ctx, entries)
			for _, e := range entries {
				if e.Warning != "" {
					fmt.Fprintf(a.Err, "warning: %s: %s\n", e.Ref.Path, e.Warning)
				}
			}
		}
	}
	return id, dir, entries, nil
}

func printHeader(w io.Writer, id string, s *config.Session, dir string) {
	if s.Name != "" {
		fmt.Fprintf(w, "%s (%s) -> %s\n", id, s.Name, dir)
		return
	}
	fmt.Fprintf(w, "%s -> %s\n", id, dir)
}

func printEntries(w io.Writer, entries []workspace.Entry) {
	if len(entries) == 0 {
		fmt.Fprintln(w, "  no repository in this session: aw add <session> <repo>")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, e := range entries {
		name := e.Project.Repo
		if e.Present() {
			name = filepath.Base(e.Dir)
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", name, e.Branch, describe(e))
	}
	tw.Flush()
}

func describe(e workspace.Entry) string {
	if e.Err != nil {
		return "error: " + strings.ReplaceAll(e.Err.Error(), "\n", " | ")
	}
	if !e.Present() {
		return "not checked out"
	}
	var parts []string
	st := e.Status
	if st.Behind > 0 {
		parts = append(parts, fmt.Sprintf("%d behind %s", st.Behind, st.Base))
	}
	if st.Upstream != "" && st.Ahead > 0 {
		parts = append(parts, fmt.Sprintf("%d ahead", st.Ahead))
	}
	if st.Upstream == "" && st.Unpushed > 0 {
		parts = append(parts, fmt.Sprintf("%d unpushed", st.Unpushed))
	}
	if st.Dirty > 0 {
		parts = append(parts, fmt.Sprintf("%d uncommitted", st.Dirty))
	}
	if e.Orphan {
		parts = append(parts, "not in config")
	}
	if e.Warning != "" {
		parts = append(parts, e.Warning)
	}
	if len(parts) == 0 {
		return "up to date"
	}
	return strings.Join(parts, ", ")
}
