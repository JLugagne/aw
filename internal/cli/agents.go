package cli

import "github.com/spf13/cobra"

const agentsGuide = `Reference for LLM agents (Claude Code, codex, ...) and scripts driving aw.

CONTEXT
  A session groups repositories. Its workspace is one directory holding one
  standalone clone per repository (origin = the real remote), created from a
  shared cache under the config "base" (<base>/<host>/<owner>/<name>). When aw launches you, your working directory
  is the session directory and these variables are set:
    AW_SESSION   id of the current session
    PWD          the session directory (same as "aw path $AW_SESSION")

NON-INTERACTIVE RULES
  Without a terminal every confirmation is answered "no", so:
  - never run "aw run" or "aw edit": they replace the process with an
    interactive program. Use "aw sync" to prepare a session.
  - pass -y to "remove"/"detach" when nothing would be lost; they still refuse
    if a repository has uncommitted changes, commits on any branch that are
    not on a remote, or stashes: deleting it loses them. Only add
    --force when the user explicitly accepted losing that work.
  - pass --pull to "sync" to fast-forward repositories that are behind,
    otherwise nothing is pulled.
  - a mistyped session id fails with: session "x" not found; did you mean: y?
    Use exact ids from "aw ls --json"; never rely on the suggestion silently.
  Errors go to stderr prefixed with "aw:" and the exit code is 1.

REPOSITORY SPECS
  host/owner/name            github.com/acme/api   (cloned over ssh, or https
                                                    if config protocol: https)
  scp-like                   git@github.com:acme/api.git
  URL                        https://... ssh://... file://...
  optional @branch suffix    github.com/acme/api@feature-x
  Without a branch the session id is the branch name: checked out from
  origin/<branch> when it exists, otherwise created from the remote default
  branch. The same branch can be used by several sessions; each has its own
  clone, so push to share commits between them.

RECIPES
  Add a repository to the current session (cloned immediately into
  $PWD/<name>):
    aw add "$AW_SESSION" github.com/acme/infra
    aw add "$AW_SESSION" github.com/acme/api@hotfix-42

  Inspect the state of every repository:
    aw show --json "$AW_SESSION"            (add --no-fetch to stay offline)

  Create and prepare a new session:
    aw create billing --name "Billing" github.com/acme/api github.com/acme/web
    aw sync billing

  Record in the config the branches checked out in the session (after a
  "git checkout" in one of its repositories):
    aw pin "$AW_SESSION"                    (add --dry-run to preview)

  Remove a repository from a session / delete a session:
    aw detach -y "$AW_SESSION" infra
    aw remove -y billing

  Locate things:
    aw ls --json          sessions
    aw path <session>     workspace directory
    aw config --path      config file (YAML, see README)

JSON OUTPUT
  aw ls --json
    [{"id", "name", "path", "exists", "projects": [{"repo", "branch"}]}]
  aw show --json <s> / aw sync --json <s>
    {"id", "name", "path", "projects": [{
       "project": {"repo", "branch"}, "cache", "branch", "dir",
       "status": {"branch", "upstream", "base", "ahead", "behind",
                  "dirty", "unpushed", "stashes"},
       "orphan", "warning", "error"}]}
    "dir" is empty when the repository is not cloned yet; "base" is the ref
    ahead/behind are computed against; "orphan" marks a repository on disk
    that is no longer in the config.
`

func (a *App) agentsTopic() *cobra.Command {
	return &cobra.Command{
		Use:   "agents",
		Short: "Reference for LLM agents and scripts (aw help agents)",
		Long:  agentsGuide,
	}
}
