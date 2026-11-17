package cli

import (
	"fmt"
	"slices"

	"github.com/spf13/cobra"

	"github.com/JLugagne/aw/internal/config"
	"github.com/JLugagne/aw/internal/repo"
)

func (a *App) pinCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "pin <session> [repo...]",
		Short: "Write the branches checked out in the session into its config",
		Long: `Read the branch currently checked out in each repository of the session
directory and store it as the project's "branch" in the config, so the next
"aw run" (or a fresh workspace) uses it. Only differing branches are written.
Repositories not cloned yet or on a detached HEAD are skipped. Give repository
names to pin only those.`,
		Example: `  aw pin billing
  aw pin billing web --dry-run
  aw pin "$AW_SESSION"`,
		Args:              cobra.MinimumNArgs(1),
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
			var selected []int
			for _, q := range args[1:] {
				idx, err := findProject(cfg, id, s, q, "")
				if err != nil {
					return err
				}
				selected = append(selected, idx)
			}
			entries, err := a.manager(cfg).Inspect(cmd.Context(), id, s, false)
			if err != nil {
				return err
			}
			changed := 0
			for i := range s.Projects {
				if len(selected) > 0 && !slices.Contains(selected, i) {
					continue
				}
				e := entries[i]
				current := e.Status.Branch
				switch {
				case e.Err != nil:
					fmt.Fprintf(a.Err, "skipping %s: %v\n", e.Project.Repo, e.Err)
					continue
				case !e.Present():
					fmt.Fprintf(a.Err, "skipping %s: not cloned yet\n", e.Project.Repo)
					continue
				case current == "":
					fmt.Fprintf(a.Err, "skipping %s: detached HEAD\n", e.Project.Repo)
					continue
				case current == effectiveBranch(id, s.Projects[i]):
					continue
				case conflicts(cfg.ProtocolName(), id, s.Projects, i, current):
					fmt.Fprintf(a.Err, "skipping %s: the session already has it on branch %s\n", e.Project.Repo, current)
					continue
				}
				fmt.Fprintf(a.Out, "%s: %s -> %s\n", e.Project.Repo, effectiveBranch(id, s.Projects[i]), current)
				s.Projects[i].Branch = current
				changed++
			}
			switch {
			case changed == 0:
				fmt.Fprintln(a.Out, "config already matches the checked-out branches")
				return nil
			case dryRun:
				fmt.Fprintf(a.Out, "dry run: %d project(s) would be updated\n", changed)
				return nil
			}
			if err := cfg.Save(path); err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "%d project(s) updated in %s\n", changed, path)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "only print what would change")
	return cmd
}

func conflicts(protocol, id string, projects []config.Project, idx int, branch string) bool {
	ref, err := repo.Parse(projects[idx].Repo, protocol)
	if err != nil {
		return false
	}
	for j, p := range projects {
		if j == idx {
			continue
		}
		other, err := repo.Parse(p.Repo, protocol)
		if err == nil && other.Path == ref.Path && effectiveBranch(id, p) == branch {
			return true
		}
	}
	return false
}
