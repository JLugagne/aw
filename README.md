# aw — agent workspaces

`aw` groups git repositories into named **sessions** and opens a coding agent
(Claude Code, opencode, codex, …) or your editor on all of them at once.

## Quick start

**1. Install**

```sh
curl -fsSL https://raw.githubusercontent.com/JLugagne/aw/main/install.sh | sh
```

This installs `aw` in `~/.local/bin` after verifying the release checksum. To
build from source instead: `go install github.com/JLugagne/aw/cmd/aw@latest`.

**2. Create a session**

```sh
aw create billing --name "Billing refactor" github.com/acme/api@feature_1 github.com/acme/web
```

This writes `~/.config/aw/config.yaml`. No config is needed beforehand: clones
default to `~/src` and the tool to `claude`.

**3. Run it**

```sh
aw run billing
```

`aw run` does five things:

- clones missing repositories
- fetches the others
- creates one worktree per repository in `~/src/.aw/sessions/billing/`
- shows which repositories are behind and asks whether to pull
- starts `claude --add-dir …` in the session directory

**4. Day to day**

```sh
aw ls                                # sessions
aw show billing                      # git state of each repository
aw add billing github.com/acme/infra # add a repository (checked out right away)
aw run --tool=opencode billing       # another tool
aw run billing -- --continue         # extra arguments for the tool
aw edit billing                      # open the session folder in your editor
cd "$(aw path billing)"
aw remove billing                    # warns about uncommitted / unpushed work first
aw upgrade                           # self-update
```

Shell completion (session ids included): `aw completion bash|zsh|fish`.

## Configuration

`~/.config/aw/config.yaml` (override with `$AW_CONFIG` or `--config`).
`aw config` opens it in your editor.

```yaml
base: ~/src                      # shared clones: <base>/<host>/<owner>/<name>
workspaces: ~/src/.aw/sessions   # optional (default shown)
tool: claude                     # default for aw run: claude, opencode, codex or any command
editor: code -n                  # aw edit; falls back to $VISUAL, $EDITOR, vi
protocol: ssh                    # ssh or https, for host/owner/name specs
tools:                           # optional: override or add tools
  claude:
    command: claude
    args: [--permission-mode, plan]
    add_dir_flag: --add-dir      # repeated before each worktree; empty = cwd only
sessions:
  billing:
    name: Billing refactor
    projects:
      - repo: github.com/acme/api
        branch: feature_1
      - repo: github.com/acme/web          # branch defaults to the session id
      - repo: git@gitlab.com:acme/infra.git
```

Unknown keys are rejected. Commands that modify the file rewrite it, which
drops comments.

## How it works

```
~/src/github.com/acme/api           shared clone, cloned once
~/src/github.com/acme/web
~/src/.aw/sessions/billing/         session directory = the tool's working directory
├── api    worktree on feature_1
└── web    worktree on billing
```

- **Worktrees.** Each session has its own worktree of every repository, so
  the same repository can belong to many sessions without being cloned again.
  Git does not allow one branch in two worktrees at once, so two sessions
  that share a repository need different branches. aw tells you when they
  collide.
- **Branches.** A project without `branch` uses the session id as its
  branch. aw checks it out from `origin/<branch>` if that exists, and
  otherwise creates it from the remote default branch.
- **Updates.** `run`, `sync` and `edit` fetch every repository first. Branches
  that are only behind can be fast-forwarded. Diverged branches are reported
  but left alone.
- **Safe removal.** `remove` and `detach` list uncommitted changes and
  commits that are on no remote, then ask before deleting. `-y` does not skip
  that question; only `--force` does. Branches are kept in the shared clone,
  so committed work stays reachable unless you pass `--delete-branches`.
- **Typos.** A mistyped session id gets a "did you mean" suggestion.
  Interactive commands offer to use it; `remove` and `detach` never guess.
- **Session context.** The tool runs with `AW_SESSION=<id>` set. A
  `CLAUDE.md` placed in the session directory applies to the whole session.

## Commands

| Command | |
|---|---|
| `aw ls [--json]` | list sessions |
| `aw show <s> [--json] [--no-fetch]` | git state of each repository |
| `aw create <s> [repo[@branch]...] [-n name] [-b branch]` | create a session |
| `aw add <s> repo[@branch]... [--no-checkout]` | add repositories |
| `aw detach <s> <repo> [-b branch] [-y] [--force] [--delete-branch]` | remove one repository |
| `aw remove <s> [-y] [--force] [--keep-config] [--delete-branches]` | delete workspace and session |
| `aw run <s> [-t tool] [--pull\|--no-pull] [--no-fetch] [-- args]` | prepare, then launch the tool |
| `aw edit <s> [-e editor]` | prepare, then open the editor |
| `aw sync <s> [--pull] [--json]` | prepare without launching anything |
| `aw path <s>` | print the session directory |
| `aw config [--path]` | edit the config file |
| `aw upgrade [--check] [--force] [--source]` | self-update from GitHub releases |

Repositories can be written as `host/owner/name`, scp-like
(`git@host:owner/name.git`) or as a URL, with an optional `@branch` suffix.

## Using aw from an agent

`aw help agents` prints a reference written for LLM agents and scripts:

- the non-interactive rules
- the JSON schemas
- recipes, such as adding a repository to the current session:

```sh
aw add "$AW_SESSION" github.com/acme/infra
```

To make an agent aware of it, add a line like this to your `CLAUDE.md`:
"to manage workspaces, run `aw help agents`".

## Install options

| Variable | Default | |
|---|---|---|
| `AW_INSTALL_DIR` | `~/.local/bin` | install directory |
| `AW_VERSION` | `latest` | e.g. `1.0.0` |

`aw upgrade` downloads the latest release for your platform, checks it
against `checksums.txt` and replaces the binary in place. `aw upgrade --source`
uses `go install` instead.
