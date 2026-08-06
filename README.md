# poppy

Fleet-wide git awareness across your machines. Poppy answers the questions you
cannot answer from the machine you happen to be sitting at:

- Which repositories are on which machine?
- Is there a dirty working tree somewhere I have forgotten about?
- Where is that branch I started on Tuesday?
- Does any work exist in exactly one place right now?

Observability, the time machine, off-machine snapshot sync, `poppy grab`,
fleet-wide secret scanning, and the read-only MCP server are implemented. Live
peer queries over Tailscale (milestone 2) and a TUI remain; see
[docs/plans](docs/plans/2026-07-20-poppy-design.md).

## How it works

Each machine runs an agent that scans configured directories for repositories
and records their state — branches, tracking, dirty files, stashes, worktrees
— as JSON in a shared git repository, the *hub*. Each machine writes only
`machines/<its-own-name>/`, so machines never conflict, and the hub's history
gives you fleet state over time for free.

State documents record paths and counts. **File contents are never recorded.**

## Install

```sh
go build -o ~/.local/bin/poppy ./cmd/poppy
```

## Set up the hub

Once, on the machine that is always on (the closet server):

```sh
git init --bare -b state ~/poppy-hub.git
```

Optionally mirror it to a private GitHub repository for off-site backup.

## Set up each machine

```sh
poppy init --hub ssh://devbox.tailnet.ts.net/~/poppy-hub.git
poppy scan
```

`poppy init` writes `~/.config/poppy/config.json`:

```json
{
  "machine": "laptop",
  "roots": ["~/code"],
  "hubRemote": "ssh://devbox.tailnet.ts.net/~/poppy-hub.git",
  "scanIntervalSeconds": 300,
  "includeWorktrees": true
}
```

Leaving `hubRemote` empty runs poppy local-only: state is still recorded and
queryable on that machine, just not shared.

Then run the agent continuously. On macOS use a launchd agent, on Linux a
systemd user unit:

```sh
poppy daemon
```

The daemon queues state locally when the hub is unreachable and publishes on
reconnect, so a laptop off the tailnet loses nothing.

## Use

```sh
poppy status              # fleet overview; --all includes clean repos
poppy dirty               # every dirty tree, oldest first
poppy dirty --files       # ...and which paths changed
poppy dirty --min-age 24h # only work that has sat for a day
poppy find auth-refactor  # where is this branch?
poppy machines            # agents, versions, heartbeats
poppy scan                # scan once now
poppy secrets             # credentials found across the fleet
poppy status --json       # for scripting
```

## Secrets

Each scan looks for credentials — and not only in the working tree. It also
reads the two places git keeps content that no remote has ever seen: **stashes**,
and **commits on local branches not reachable from any remote-tracking branch**.
That content exists on exactly one disk, is invisible from every other machine,
and is precisely what a pre-commit hook can never help with. Poppy is the only
thing in the toolchain that can enumerate it fleet-wide.

```sh
poppy secrets                     # every finding, unreachable content first
poppy secrets --origin stash      # only what is stashed
poppy secrets --rule github-token
poppy secrets --machine devbox
poppy secrets --json
```

**A finding is a location and nothing else** — repository, path, rule id, and
line, plus the stash selector or commit SHA that holds it. The matched value is
never recorded, not even masked or truncated. State documents are committed to
a shared hub, so a scanner that wrote a secret into one would be strictly worse
than no scanner at all. The `Finding` type has no exported field and no
constructor that accepts content, so there is nowhere for a matched value to go
even by mistake; a document read back from the hub with an extra field carrying
one drops it on unmarshal.

Rules are few and chosen for precision, because a scanner people mute is worth
nothing: private key blocks, AWS access keys, GitHub tokens, `sk-ant-` and other
`sk-` provider keys, Slack tokens and webhooks, live Stripe keys, and
assignments to `*KEY`/`TOKEN`/`SECRET`/`PASSWORD` in `.env`-shaped files.
Template files (`.env.example`, `.env.sample`, `.env.template`, `.env.dist`) are
exempt from the assignment rule only — a name ending in `.example` excuses a
placeholder, never a real `ghp_` token.

Ignored files are not scanned. `.gitignore` is poppy's secrets boundary
everywhere else, and reading past it here would report on files the rest of the
tool promises never to touch.

Every bound a scan applies is reported rather than silently truncating, so a
quiet repository means a clean repository and not an exhausted budget. If
`gitleaks` is on PATH, `"gitleaks": true` runs it over each working tree as
well; it is off by default so two machines cannot disagree about the same
repository because of what happens to be installed on each. Both backends obey
the no-values rule — poppy reads only the rule, file, and line from a gitleaks
report and discards the rest.

## Hooks

The hook is the cheap half: it stops the *next* credential. Its value here is
that poppy knows about every repository on every machine, so "which of mine are
unprotected" is a question with an answer.

```sh
poppy hooks verify            # which repositories are unprotected
poppy hooks install           # install into every configured repository
poppy hooks install --force   # ...replacing hooks somebody else wrote
poppy hooks uninstall
```

Installing never clobbers a hook a person wrote. Such a repository is reported
and left exactly as it was; `--force` replaces it and keeps the original beside
it as `pre-commit.poppy-backup`. The hook fails open when poppy is not on PATH —
uninstalling poppy should not make every commit in every repository start
failing — and says so loudly on stderr.

## Time machine

Every scan captures each dirty working tree as a git commit stored outside the
branch graph, so uncommitted work survives a bad `checkout`, `reset`, `clean`,
or rebase. Capture never touches HEAD, the index, or the working tree — it is
invisible to whoever is working in the repository.

```sh
poppy snapshot                     # capture this repository now
poppy timeline                     # what has been captured
poppy restore                      # recover the newest snapshot
poppy restore --as-of 2h           # ...or whatever existed two hours ago
poppy restore --as-of "2026-07-19 16:00"
```

`--as-of` accepts relative durations (`30m`, `2h`, `3d`) and absolute times.
Restore refuses to apply over a working tree that has its own uncommitted
changes, since that would merge two sets of work with no way to separate them
afterwards; pass `--force` to override.

Snapshots use git's own stash commit shape, so the staged, unstaged, and
untracked split is restored exactly as captured, and `git stash apply <sha>`
works on one directly without poppy.

**What is never captured:** anything a `.gitignore` excludes. That makes
`.gitignore` the secrets boundary — `.env` files stay where they are. Untracked
files larger than `snapshot.maxFileBytes` (10 MB by default) are skipped and
reported rather than bloating the object store.

## Off-machine sync and teleport

With a mirror configured, each scan pushes new snapshots to a bare repository
on the closet server — one per project, which doubles as an off-site backup.
Because a snapshot carries its base history, a machine that has never seen a
project can still recover another machine's uncommitted work from it.

```json
"snapshot": {
  "enabled": true,
  "mirror": { "enabled": true, "host": "devbox.tailnet.ts.net", "dir": "/home/mads/poppy-mirrors" }
}
```

Leave `host` empty to mirror to a local path instead (a single-box setup).

Then teleport work between machines:

```sh
poppy grab devbox:poppy            # bring devbox's WIP for `poppy` onto this machine
poppy grab devbox:poppy feature-x  # ...a specific branch
```

Grab fetches the source machine's latest snapshot and applies it into your
local checkout of the same project, reproducing its exact working state — the
staged, unstaged, and untracked split intact. It refuses to apply over a
working tree with its own changes, or onto a branch that has diverged from the
snapshot's base, explaining what it found; `--force` overrides.

Because the work then exists on two machines, `poppy status` flags any project
left dirty on the same branch in more than one place, so you commit before the
two copies drift apart.

Sample:

```
FLEET  2 machines · 4 repos · 1 dirty · 1 unpushed · 1 stashed

devbox  up · just now  2 repos · linux/amd64
  api    main                   clean             1 stash
  poppy  feature/auth-refactor  2 files dirty 2d  never pushed

laptop (this machine)  up · just now  2 repos · darwin/arm64
  all clean
```

## What poppy considers "at risk"

Work that exists in exactly one place:

- uncommitted changes in a working tree
- commits ahead of their upstream, or branches never pushed at all
- stashes
- repositories with no remote configured
- working trees git can no longer read, such as an **orphaned worktree** whose
  parent repository was moved or removed. Its contents are invisible to every
  ordinary git command, which makes it the most thoroughly stranded work there
  is. Repair one with `git worktree repair` from the parent repository.

## Ask an agent

`poppy mcp` serves the fleet state to any agent over the Model Context Protocol
(read-only), so Claude on one machine can answer questions about all of them:

- `fleet_status` — every machine, last seen, and its dirty / unpushed / backed-up counts
- `dirty_trees` — all uncommitted work fleet-wide, oldest first
- `find_work` — locate a repository or branch across machines
- `repo_state` — the full state of one repository
- `snapshot_history` — the time machine for a repository
- `secret_findings` — where credentials are, including in stashes and unpushed commits
- `where_was_i` — a narrative digest of recent activity

Register it with Claude Code:

```sh
claude mcp add poppy -- poppy mcp
```

Or add it to a project's `.mcp.json`:

```json
{
  "mcpServers": {
    "poppy": { "command": "poppy", "args": ["mcp"] }
  }
}
```

Then ask, from any machine: *"what was I working on on the devbox?"*

## Configuration

| Key | Meaning |
|-----|---------|
| `machine` | name in the fleet; defaults to the hostname |
| `roots` | directories to scan; `~` is expanded |
| `hubRemote` | git URL of the shared hub; empty means local-only |
| `scanIntervalSeconds` | how often the daemon rescans |
| `skipDirs` | extra directory names never descended into |
| `maxDepth` | how deep beneath a root to look (default 8) |
| `includeWorktrees` | report linked worktrees as their own entries |
| `staleAfterSeconds` | when a machine's heartbeat is considered stale |
| `snapshot.enabled` | capture uncommitted work each scan |
| `snapshot.maxFileBytes` | largest untracked file to capture (default 10 MB) |
| `snapshot.keepAllHours` | retain every snapshot this long (default 168) |
| `snapshot.keepDailyHours` | then one per day this long (default 2160) |
| `snapshot.excludeRepos` | glob patterns of repositories never to capture |
| `snapshot.mirror.enabled` | push snapshots off the machine each scan |
| `snapshot.mirror.host` | SSH target of the closet server (empty = local path) |
| `snapshot.mirror.dir` | directory holding the bare per-repo mirrors |
| `secrets.enabled` | scan for credentials each cycle (default true) |
| `secrets.gitleaks` | also run gitleaks over each working tree when installed |
| `secrets.intervalSeconds` | longest a result is reused for an unchanged repo (default 3600) |
| `secrets.maxFileBytes` | largest file or object examined (default 512 KB) |
| `secrets.maxWorktreeFiles` | most working-tree files read per repo (default 20000) |
| `secrets.maxUnpushedCommits` | most unpushed commits walked per repo (default 500) |
| `secrets.maxBlobs` | most distinct stash/commit objects read (default 5000) |
| `secrets.maxFindings` | most findings recorded per repo (default 200) |
| `secrets.timeoutSeconds` | bound on one repository's scan (default 60) |
| `secrets.excludeRepos` | glob patterns of repositories never scanned |

`node_modules`, `vendor`, `.venv`, `target`, and similar directories are always
skipped. Nested repositories below a repository root are treated as submodules
or vendored copies and are not tracked separately.

Environment overrides, useful for testing: `POPPY_CONFIG`, `POPPY_DATA_DIR`.

## Development

```sh
go test ./...          # integration tests run against real git repositories
go test -race ./...
```
