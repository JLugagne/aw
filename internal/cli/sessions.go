package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/JLugagne/aw/internal/config"
	"github.com/JLugagne/aw/internal/repo"
	"github.com/JLugagne/aw/internal/workspace"
)

type sessionJSON struct {
	ID       string           `json:"id"`
	Name     string           `json:"name,omitempty"`
	Path     string           `json:"path"`
	Exists   bool             `json:"exists"`
	Projects []config.Project `json:"projects"`
}

func (a *App) lsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List sessions",
		Long: `List sessions. --json prints an array of
{id, name, path, exists, projects: [{repo, branch}]}; "exists" tells whether
the workspace directory has been created.`,
		Example: `  aw ls
  aw ls --json | jq -r '.[].id'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, _, err := a.load()
			if err != nil {
				return err
			}
			mgr := a.manager(cfg)
			list := make([]sessionJSON, 0, len(cfg.Sessions))
			for _, id := range sortedIDs(cfg) {
				s := cfg.Sessions[id]
				dir, err := mgr.SessionDir(id)
				if err != nil {
					return err
				}
				_, statErr := os.Stat(dir)
				projects := s.Projects
				if projects == nil {
					projects = []config.Project{}
				}
				list = append(list, sessionJSON{ID: id, Name: s.Name, Path: dir, Exists: statErr == nil, Projects: projects})
			}
			if asJSON {
				enc := json.NewEncoder(a.Out)
				enc.SetIndent("", "  ")
				return enc.Encode(list)
			}
			if len(list) == 0 {
				fmt.Fprintln(a.Out, "no session yet: aw create <id> <repo>...")
				return nil
			}
			tw := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tREPOS\tWORKSPACE")
			for _, s := range list {
				ws := "-"
				if s.Exists {
					ws = s.Path
				}
				fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", s.ID, s.Name, len(s.Projects), ws)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print sessions as JSON")
	return cmd
}

func (a *App) showCmd() *cobra.Command {
	var noFetch, asJSON bool
	cmd := &cobra.Command{
		Use:     "show <session>",
		Aliases: []string{"status"},
		Short:   "Show the git state of every repository of a session",
		Long: `Fetch (unless --no-fetch) and report, for every repository of the session,
its directory, branch, commits behind/ahead of its upstream (or of the remote
default branch when it has none), uncommitted paths and commits absent from
every remote, and stashes. Repositories found in the session directory but no
longer configured are flagged.`,
		Example: `  aw show billing
  aw show --json --no-fetch "$AW_SESSION"`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: a.completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := a.load()
			if err != nil {
				return err
			}
			id, s, err := a.resolve(cfg, args[0], true)
			if err != nil {
				return err
			}
			mgr := a.manager(cfg)
			entries, err := mgr.Inspect(cmd.Context(), id, s, !noFetch)
			if err != nil {
				return err
			}
			dir, err := mgr.SessionDir(id)
			if err != nil {
				return err
			}
			if asJSON {
				return writeEntriesJSON(a.Out, id, s, dir, entries)
			}
			printHeader(a.Out, id, s, dir)
			printEntries(a.Out, entries)
			return nil
		},
	}
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "do not fetch remotes before computing the state")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the state as JSON")
	return cmd
}

func writeEntriesJSON(w io.Writer, id string, s *config.Session, dir string, entries []workspace.Entry) error {
	type entryJSON struct {
		workspace.Entry
		Error string `json:"error,omitempty"`
	}
	out := struct {
		ID       string      `json:"id"`
		Name     string      `json:"name,omitempty"`
		Path     string      `json:"path"`
		Projects []entryJSON `json:"projects"`
	}{ID: id, Name: s.Name, Path: dir, Projects: []entryJSON{}}
	for _, e := range entries {
		j := entryJSON{Entry: e}
		if e.Err != nil {
			j.Error = e.Err.Error()
		}
		out.Projects = append(out.Projects, j)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func (a *App) createCmd() *cobra.Command {
	var name, branch string
	cmd := &cobra.Command{
		Use:   "create <session> [repo[@branch]...]",
		Short: "Create a session, optionally with repositories",
		Long: `Create a session. Repositories are written as host/owner/name
(github.com/org/api), scp-like (git@github.com:org/api.git) or URL, with an
optional @branch suffix. Without a branch, the session id is used.
Nothing is cloned until "aw run" or "aw sync".`,
		Example: `  aw create billing --name "Billing refactor" github.com/acme/api@feature_1 github.com/acme/web
  aw create spike -b spike-cache github.com/acme/api github.com/acme/cache`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, path, err := a.load()
			if err != nil {
				return err
			}
			s, err := cfg.AddSession(args[0], name)
			if err != nil {
				return errors.Join(fmt.Errorf("create %q", args[0]), err)
			}
			if err := addProjects(cfg, args[0], s, args[1:], branch, a.Out); err != nil {
				return err
			}
			if err := cfg.Save(path); err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "session %s created with %d repositories; open it with: aw run %s\n", args[0], len(s.Projects), args[0])
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "name", "n", "", "human readable name")
	cmd.Flags().StringVarP(&branch, "branch", "b", "", "branch for repositories given without @branch")
	return cmd
}

func (a *App) addCmd() *cobra.Command {
	var branch string
	var noCheckout bool
	cmd := &cobra.Command{
		Use:   "add <session> <repo[@branch]>...",
		Short: "Add repositories to a session",
		Long: `Add repositories to a session. When the session workspace already exists
the repositories are cloned into it right away,
so a running agent can use them immediately (they live under the session
directory, its working directory).`,
		Example: `  aw add billing github.com/acme/infra
  aw add billing github.com/acme/api@hotfix-42 git@gitlab.com:acme/tools.git
  aw add "$AW_SESSION" github.com/acme/infra   # from inside a session`,
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: a.completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, path, err := a.load()
			if err != nil {
				return err
			}
			id, s, err := a.resolve(cfg, args[0], true)
			if err != nil {
				return err
			}
			if err := addProjects(cfg, id, s, args[1:], branch, a.Out); err != nil {
				return err
			}
			if err := cfg.Save(path); err != nil {
				return err
			}
			mgr := a.manager(cfg)
			dir, err := mgr.SessionDir(id)
			if err != nil {
				return err
			}
			if _, statErr := os.Stat(dir); noCheckout || statErr != nil {
				fmt.Fprintf(a.Out, "repositories are cloned on the next: aw run %s (or aw sync %s)\n", id, id)
				return nil
			}
			entries, err := mgr.Prepare(cmd.Context(), id, s, false)
			if err != nil {
				return err
			}
			printHeader(a.Out, id, s, dir)
			printEntries(a.Out, entries)
			if n := countFailed(entries); n > 0 {
				return fmt.Errorf("session saved, but %d repositories could not be checked out", n)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&branch, "branch", "b", "", "branch for repositories given without @branch")
	cmd.Flags().BoolVar(&noCheckout, "no-checkout", false, "only update the config, do not clone now")
	return cmd
}

func countFailed(entries []workspace.Entry) int {
	n := 0
	for _, e := range entries {
		if e.Err != nil {
			n++
		}
	}
	return n
}

func effectiveBranch(id string, p config.Project) string {
	if p.Branch != "" {
		return p.Branch
	}
	return id
}

func addProjects(cfg *config.Config, id string, s *config.Session, specs []string, defaultBranch string, out io.Writer) error {
	for _, spec := range specs {
		r, b := repo.SplitSpec(spec)
		if b == "" {
			b = defaultBranch
		}
		ref, err := repo.Parse(r, cfg.ProtocolName())
		if err != nil {
			return errors.Join(fmt.Errorf("add %q", spec), err)
		}
		p := config.Project{Repo: r, Branch: b}
		duplicate := false
		for _, existing := range s.Projects {
			eref, err := repo.Parse(existing.Repo, cfg.ProtocolName())
			if err == nil && eref.Path == ref.Path && effectiveBranch(id, existing) == effectiveBranch(id, p) {
				duplicate = true
				break
			}
		}
		if duplicate {
			fmt.Fprintf(out, "skipping %s: already in session %s on branch %s\n", r, id, effectiveBranch(id, p))
			continue
		}
		s.Projects = append(s.Projects, p)
		fmt.Fprintf(out, "added %s (branch %s)\n", r, effectiveBranch(id, p))
	}
	return nil
}

func (a *App) detachCmd() *cobra.Command {
	var branch string
	var yes, force bool
	cmd := &cobra.Command{
		Use:     "detach <session> <repo>",
		Aliases: []string{"rm-repo"},
		Short:   "Remove a repository from a session and delete its clone",
		Long: `Remove a repository from a session. <repo> is the spec as written in the
config, its host/owner/name path or just its name. Uncommitted or unpushed
work triggers a warning and an extra confirmation.`,
		Example: `  aw detach billing web
  aw detach billing github.com/acme/api --branch hotfix-42 -y`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: a.completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, path, err := a.load()
			if err != nil {
				return err
			}
			id, s, err := a.resolve(cfg, args[0], false)
			if err != nil {
				return err
			}
			idx, err := findProject(cfg, id, s, args[1], branch)
			if err != nil {
				return err
			}
			mgr := a.manager(cfg)
			entries, err := mgr.Inspect(cmd.Context(), id, s, false)
			if err != nil {
				return err
			}
			e := entries[idx]
			if e.Present() {
				if !a.checkLocalWork([]workspace.Entry{e}, yes, force, fmt.Sprintf("Detach %s from %s and delete %s?", e.Project.Repo, id, e.Dir)) {
					return a.aborted()
				}
				if err := os.RemoveAll(e.Dir); err != nil {
					return err
				}
			}
			s.Projects = append(s.Projects[:idx], s.Projects[idx+1:]...)
			if err := cfg.Save(path); err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "%s detached from %s\n", e.Project.Repo, id)
			return nil
		},
	}
	cmd.Flags().StringVarP(&branch, "branch", "b", "", "select the project by branch when the repository appears several times")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation when there is no local work")
	cmd.Flags().BoolVar(&force, "force", false, "do not ask for confirmation even if local work would be lost")
	return cmd
}

func findProject(cfg *config.Config, id string, s *config.Session, query, branch string) (int, error) {
	var matches []int
	qref, qerr := repo.Parse(query, cfg.ProtocolName())
	for i, p := range s.Projects {
		if branch != "" && effectiveBranch(id, p) != branch {
			continue
		}
		ref, err := repo.Parse(p.Repo, cfg.ProtocolName())
		switch {
		case p.Repo == query:
		case err == nil && qerr == nil && ref.Path == qref.Path:
		case err == nil && ref.Name() == query:
		default:
			continue
		}
		matches = append(matches, i)
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		repos := make([]string, 0, len(s.Projects))
		for _, p := range s.Projects {
			repos = append(repos, p.Repo)
		}
		return 0, fmt.Errorf("repository %q not found in session %s (has: %s)", query, id, strings.Join(repos, ", "))
	}
	return 0, fmt.Errorf("repository %q matches %d projects in session %s; use --branch", query, len(matches), id)
}

func (a *App) checkLocalWork(entries []workspace.Entry, yes, force bool, question string) bool {
	var risky []workspace.Entry
	for _, e := range entries {
		if e.Present() && (e.Err != nil || e.Status.HasLocalWork()) {
			risky = append(risky, e)
		}
	}
	if len(risky) == 0 {
		return yes || a.confirm(question, false)
	}
	fmt.Fprintln(a.Err, "WARNING: the following repositories hold work that is not on any remote:")
	for _, e := range risky {
		var parts []string
		if e.Err != nil {
			parts = append(parts, "cannot inspect: "+e.Err.Error())
		}
		if e.Status.Dirty > 0 {
			parts = append(parts, fmt.Sprintf("%d uncommitted change(s) (lost)", e.Status.Dirty))
		}
		if e.Status.Unpushed > 0 {
			parts = append(parts, fmt.Sprintf("%d unpushed commit(s)", e.Status.Unpushed))
		}
		if e.Status.Stashes > 0 {
			parts = append(parts, fmt.Sprintf("%d stash(es)", e.Status.Stashes))
		}
		fmt.Fprintf(a.Err, "  %s: %s\n", e.Dir, strings.Join(parts, ", "))
	}
	return force || a.confirm(question+" Local work will be lost", false)
}

func (a *App) removeCmd() *cobra.Command {
	var yes, force, keepConfig bool
	cmd := &cobra.Command{
		Use:     "remove <session>",
		Aliases: []string{"rm", "delete"},
		Short:   "Delete a session and its workspace",
		Long: `Delete the session workspace (its repositories) and remove it from the
config. Every repository is a standalone clone, so uncommitted changes,
commits on any branch that are not on a remote, and stashes are lost: they
trigger a warning and a confirmation that only --force skips. The shared
caches under the base directory are kept.`,
		Example: `  aw remove billing
  aw remove billing -y                 # no prompt if nothing would be lost
  aw remove billing --keep-config -y   # free the disk, keep the definition`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: a.completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, path, err := a.load()
			if err != nil {
				return err
			}
			id, s, err := a.resolve(cfg, args[0], false)
			if err != nil {
				return err
			}
			mgr := a.manager(cfg)
			dir, err := mgr.SessionDir(id)
			if err != nil {
				return err
			}
			entries, err := mgr.Inspect(cmd.Context(), id, s, false)
			if err != nil {
				return err
			}
			question := fmt.Sprintf("Remove session %s?", id)
			if _, statErr := os.Stat(dir); statErr == nil {
				question = fmt.Sprintf("Remove session %s and delete %s?", id, dir)
			}
			if !a.checkLocalWork(entries, yes, force, question) {
				return a.aborted()
			}
			if err := mgr.Remove(id); err != nil {
				return err
			}
			if !keepConfig {
				delete(cfg.Sessions, id)
				if err := cfg.Save(path); err != nil {
					return err
				}
			}
			fmt.Fprintf(a.Out, "session %s removed\n", id)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation when there is no local work")
	cmd.Flags().BoolVar(&force, "force", false, "do not ask for confirmation even if local work would be lost")
	cmd.Flags().BoolVar(&keepConfig, "keep-config", false, "only delete the workspace, keep the session definition")
	return cmd
}

func (a *App) pathCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "path <session>",
		Short:             "Print the workspace directory of a session (cd \"$(aw path s)\")",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: a.completeSessions,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := a.load()
			if err != nil {
				return err
			}
			id, _, err := a.resolve(cfg, args[0], true)
			if err != nil {
				return err
			}
			dir, err := a.manager(cfg).SessionDir(id)
			if err != nil {
				return err
			}
			fmt.Fprintln(a.Out, dir)
			return nil
		},
	}
}
