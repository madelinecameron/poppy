# poppy

Fleet-wide git awareness across your machines. Poppy answers the questions you
cannot answer from the machine you happen to be sitting at:

- Which repositories are on which machine?
- Is there a dirty working tree somewhere I have forgotten about?
- Where is that branch I started on Tuesday?
- Does any work exist in exactly one place right now?

Observability and the local time machine are implemented. Snapshot sync,
`poppy grab`, and the MCP server are designed in
[docs/plans](docs/plans/2026-07-20-poppy-design.md) and land next.

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
poppy status --json       # for scripting
```

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

Snapshots currently stay on the machine that took them. That protects against
losing work to git, but not against losing the machine — see the sync decision
in the design document.

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

`node_modules`, `vendor`, `.venv`, `target`, and similar directories are always
skipped. Nested repositories below a repository root are treated as submodules
or vendored copies and are not tracked separately.

Environment overrides, useful for testing: `POPPY_CONFIG`, `POPPY_DATA_DIR`.

## Development

```sh
go test ./...          # integration tests run against real git repositories
go test -race ./...
```
