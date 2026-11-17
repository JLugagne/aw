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

`aw run` does four things:

- clones each repository into `~/src/.aw/sessions/billing/` (cheap: objects
  are hardlinked from a shared cache), or fetches it if already there
- shows which repositories are behind and asks whether to pull
- starts `claude --add-dir …` in the session directory, with each
  repository's `CLAUDE.md` loaded
- prints how to resume: `aw run -c billing`

**4. Day to day**

```sh
aw ls                                # sessions
aw show billing                      # git state of each repository
aw add billing github.com/acme/infra # add a repository (checked out right away)
aw run -c billing                    # resume the latest conversation, same directories
aw run billing -- --resume <id>      # resume a given conversation
aw run --tool=opencode billing       # another tool
aw edit billing                      # open the session folder in your editor
aw pin billing                       # save the branches checked out in the session into the config
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
tools:                           # optional: overlay the built-in tools or add new ones
  claude:
    args: [--permission-mode, plan]
    # built-in: command: claude, add_dir_flag: --add-dir, continue_args: [--continue],
    #           env: {CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD: "1"}
  mytool:
    command: mytool
    add_dir_flag: --dir          # repeated before each repository; empty = cwd only
    continue_args: [--resume-last]
    env: {MYTOOL_MODE: agent}    # defaults: never override your environment
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
~/src/github.com/acme/api           shared cache, cloned once (can be your own working copy)
~/src/github.com/acme/web
~/src/.aw/sessions/billing/         session directory = the tool's working directory
├── api    standalone clone on feature_1, origin = github.com/acme/api
└── web    standalone clone on billing
```

- **One clone per session.** Each session repository is a full, independent
  clone. Its objects are hardlinked from the shared cache (copied if on
  another filesystem) and `origin` points to the real remote, so creating it
  costs almost no disk or network. Any branch can be checked out in as many
  sessions as you like; push to share commits between them.
- **Branches.** A project without `branch` uses the session id as its
  branch. aw checks it out from `origin/<branch>` if that exists, and
  otherwise creates it from the remote default branch.
- **Updates.** `run`, `sync` and `edit` fetch every repository first. Branches
  that are only behind can be fast-forwarded. Diverged branches are reported
  but left alone.
- **Safe removal.** Deleting a session deletes its clones. Before that,
  `remove` and `detach` list uncommitted changes, commits that are on no
  remote (on any branch) and stashes, then ask for confirmation. `-y` does not
  skip that question; only `--force` does. The shared caches are kept.
- **Typos.** A mistyped session id gets a "did you mean" suggestion.
  Interactive commands offer to use it; `remove` and `detach` never guess.
- **Claude Code.** claude is started with `--add-dir` for every repository
  and `CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1` (unless you set it
  yourself), so each repository's `CLAUDE.md` is loaded. A `CLAUDE.md` in
  the session directory applies to the whole session. `AW_SESSION=<id>` is
  exported.
- **Resuming.** `--add-dir` directories are not restored when a conversation
  is resumed with `claude --continue` alone. Resume through aw instead:
  `aw run -c <s>` or `aw run <s> -- --resume <id>`.

## Commands

| Command | |
|---|---|
| `aw ls [--json]` | list sessions |
| `aw show <s> [--json] [--no-fetch]` | git state of each repository |
| `aw create <s> [repo[@branch]...] [-n name] [-b branch]` | create a session |
| `aw add <s> repo[@branch]... [--no-checkout]` | add repositories |
| `aw detach <s> <repo> [-b branch] [-y] [--force]` | remove one repository |
| `aw remove <s> [-y] [--force] [--keep-config]` | delete workspace and session |
| `aw run <s> [-c] [-t tool] [--pull\|--no-pull] [--no-fetch] [-- args]` | prepare, then launch the tool |
| `aw edit <s> [-e editor]` | prepare, then open the editor |
| `aw sync <s> [--pull] [--json]` | prepare without launching anything |
| `aw pin <s> [repo...] [--dry-run]` | write the checked-out branches into the session config |
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
