# Poppy — Design

**Date:** 2026-07-20
**Status:** Draft, scope agreed

Distributed git fleet awareness and work teleportation across personal machines.

## Problem

Work happens on three machines — a roaming laptop, a dev server in the closet
(reachable via Tailscale), and a desktop PC. Uncommitted work gets stranded:
a dirty tree on one machine is invisible from the others and exists in exactly
one place. Poppy makes every repo's state on every machine visible everywhere,
and makes uncommitted work snapshotted, backed up, and fetchable from any
machine.

## Goals

1. **Observability** — fleet-wide answer to "what repos are where, what
   branches, what's dirty, what's unpushed?"
2. **Teleport** — `poppy grab` pulls another machine's uncommitted work onto
   the current machine, safely.
3. **Time machine** — automatic snapshots of dirty trees; any working state
   from the last N days is recoverable, even after hardware loss.
4. **Agent-queryable** — an MCP server so Claude (or any agent) can answer
   "what was I working on on the devbox?"

## Non-goals

- Not a git host, not a sync tool (no Syncthing-style file mirroring).
- Never touches real branches or history; all poppy writes live under
  `refs/poppy/*` namespaces.
- No multi-user support. This is a single-person, single-tailnet tool.

## Architecture

Three components, one Go binary (`poppy`) plus a bare repo:

```
┌──────────┐  push state +      ┌─────────────────┐
│ poppyd   │  WIP snapshot refs │  poppy-hub      │   mirror    ┌────────────┐
│ (laptop) │───────────────────▶│  bare git repo  │────────────▶│ GitHub     │
└──────────┘                    │  (closet server)│             │ (private,  │
┌──────────┐                    └─────────────────┘             │  off-site) │
│ poppyd   │───────────────────▶        ▲                       └────────────┘
│ (devbox) │                            │ fetch state + snapshots
└──────────┘                    ┌───────┴───────┐
┌──────────┐                    │ poppy CLI/TUI │
│ poppyd   │◀──── tsnet ───────▶│ poppy mcp     │
│ (desktop)│  live queries      └───────────────┘
└──────────┘
```

1. **`poppyd`** — agent daemon on each machine. Scans configured roots for
   repos, watches for changes, snapshots dirty trees, pushes state and
   snapshot refs to the hub. Embeds `tsnet` and serves a small HTTP API to
   the tailnet.
2. **`poppy-hub`** — a bare git repo on the closet server. Source of truth
   for last-known state and all WIP snapshots. Mirrored to a private GitHub
   repo for off-site backup.
3. **`poppy` CLI / TUI / MCP server** — subcommands of the same binary.
   Reads the hub for last-known state; queries peers over tsnet for live
   state when they're online.

### Why git-as-database (option C)

- Per-machine directories in the state branch mean writes never conflict.
- History of fleet state is `git log` — event sourcing for free.
- Sync, auth (SSH), and offline queuing are solved problems.
- The teleport substrate falls out naturally: WIP snapshots are just commits
  on namespaced refs pushed to the same hub.

### Why the thin tsnet layer (option B garnish)

The hub gives last-known state; tsnet gives live state. `poppy status` fans
out to online peers (`/v1/state`) with a short timeout and falls back to hub
data with a staleness marker ("desktop, last seen 2d ago"). `poppy grab`
uses it to request a final fresh snapshot before fetching. Tailnet ACLs are
the only auth layer needed.

## State store

Hub repo, branch `state`:

```
machines/<hostname>/manifest.json          # OS, agent version, heartbeat ts, scan roots
machines/<hostname>/repos/<path-id>.json   # one state doc per working tree
```

Two distinct identities, because one key cannot do both jobs:

- **`repoId`** — hash of the canonicalized remote URL. Correlates the same
  project across machines regardless of local path. `git@github.com:mads/poppy.git`,
  `https://github.com/mads/poppy`, and `ssh://git@github.com/mads/poppy.git`
  all resolve to one id. Falls back to a path hash when there is no remote.
- **`path-id`** (the file key) — hash of machine plus absolute path. Several
  worktrees or clones of one project share a `repoId`, so keying documents by
  `repoId` would have them overwrite each other.

Paths are canonicalized through `filepath.EvalSymlinks` at scan time to match
what git reports as a working tree root. Without this, a symlinked code
directory yields a different key each scan and dirty ages reset continuously.

Repo state doc:

```json
{
  "repoId": "a1b2c3d4",
  "path": "/home/mads/code/personal/poppy",
  "remotes": {"origin": "git@github.com:mads/poppy.git"},
  "head": {"branch": "main", "sha": "…"},
  "branches": [
    {"name": "main", "sha": "…", "upstream": "origin/main", "ahead": 2, "behind": 0}
  ],
  "dirty": {"staged": 1, "unstaged": 4, "untracked": 2, "files": ["src/agent.go", "…"]},
  "dirtySince": "2026-07-19T14:02:11Z",
  "stashes": 1,
  "worktrees": ["/home/mads/code/personal/poppy-wt-fix"],
  "lastCommit": {"sha": "…", "subject": "feat(agent): add fsmonitor", "when": "…"},
  "lastFetch": "2026-07-20T09:00:00Z",
  "wipRef": "refs/poppy/wip/devbox/main",
  "claim": null,
  "updatedAt": "2026-07-20T13:57:00Z",
  "seq": 4182
}
```

State docs contain paths and file *names* only, never file contents. Contents
exist only as git objects reachable from WIP refs.

Agents commit state changes locally and push with a rebase-and-retry loop.
When the hub is unreachable (laptop offline), commits queue locally and push
on reconnect. `seq` is a per-machine monotonic counter so consumers can order
updates without trusting wall clocks.

## WIP snapshots (time machine)

**Trigger:** filesystem events (fsmonitor/watchman) debounced to 2 minutes of
quiet, capped by a 10-minute max interval while dirty, plus on-demand
(`poppy snapshot`, or a peer request during `grab`).

**Mechanism:** `git stash create` semantics via plumbing — build tree objects
from the index and working tree without touching HEAD, index, or the working
tree itself. The snapshot commit uses the stash commit format:

- parent 1: current HEAD
- parent 2: commit of the index tree
- parent 3: commit of untracked (not ignored) files

This keeps staged/unstaged/untracked distinction intact and stays
interoperable with `git stash apply` tooling.

**Ref layout.** Two namespaces, both under `refs/poppy` so poppy can never
disturb a branch or tag:

```
refs/poppy/wip/<machine>/<branch>            latest snapshot
refs/poppy/snap/<machine>/<branch>/<millis>  every retained snapshot
```

Explicit per-snapshot refs rather than a reflog: reflog entries for commits
unreachable from any branch expire under `gc.reflogExpireUnreachable` (30 days
by default), which would silently delete history the time machine promised to
keep. Refs protect their objects from `git gc` unconditionally.

Two details the implementation forced:

- **Branch names are escaped** (`/` → `%2F`) before use as a ref component.
  Git cannot store a ref at a path that is also a directory, so a snapshot ref
  for branch `feature` blocks one for `feature/auth`. Git prevents both
  branches existing at once, but nothing stops the first being deleted and the
  second created later, at which point the stale snapshot ref causes a hard
  failure.
- **Capture times are recorded in milliseconds.** Second precision makes two
  captures within one second name the same ref, and the second silently
  replaces the first. Refs are additionally probed for collision before use.

**Privacy:** gitignored files are never captured, which makes `.gitignore` the
secrets boundary — `.env` and friends stay put. Untracked files above
`snapshot.maxFileBytes` (10 MB default) are skipped and reported, so stray
build output cannot permanently bloat a repository's object store. Repositories
can be excluded wholesale with `snapshot.excludeRepos` glob patterns.

**Retention:** keep every snapshot for 7 days, then the newest per day for 90
days, then drop. The newest snapshot of a branch is always retained however
old it is: it is the only record of that work, and pruning it would delete the
very thing poppy exists to protect. Agents prune each cycle.

**Sync:** *implemented — per-repo bare mirrors.* Each project has a bare
mirror on the closet server, one per project, keyed by `repoId` so every
machine working on a project shares its mirror. The agent pushes only that
machine's `refs/poppy/*` after each capture; because a snapshot commit's first
parent is HEAD, the push carries the whole object closure, so a machine that
has never seen a project can still fetch and apply another's uncommitted work.

Why per-repo mirrors over the alternatives considered: a single shared hub
would hold none of a project's base objects, so the first push of every project
would transfer its entire history into one enormous repo. Pushing snapshots to
each project's own `origin` would write poppy's refs onto shared remotes,
including clients'. Per-repo mirrors keep client code on the owner's hardware
and double as a complete off-site backup.

Refs are pushed with two refspecs, one per namespace
(`refs/poppy/wip/<machine>/*` and `refs/poppy/snap/<machine>/*`), because a git
refspec permits only one wildcard. Mirrors are created on demand: locally with
`git init --bare`, or over SSH with `mkdir -p && git init --bare`, which is
idempotent so two machines racing to create one is safe. When no host is
configured the mirror directory is treated as a local path, which suits a
single-box setup and makes the push/fetch paths testable without SSH.

## `poppy grab`

`poppy grab <machine>:<repo> [branch]` — teleport work from another machine.
*Implemented.*

1. Resolve `<machine>:<repo>` against fleet state: find the source machine's
   snapshot (which must be synced to the mirror) and this machine's checkout
   of the same project, matched by `repoId`. Grab applies into an existing
   checkout rather than cloning, so the project must already be present.
2. Fetch the source machine's `refs/poppy/*` from the project's mirror. Only
   that machine's namespace is fetched, so this machine's own snapshots are
   untouched.
3. Place the target branch on the snapshot's base — the source's HEAD at
   capture, which arrived with the fetch — creating the branch there if it is
   absent. Refuse if the local tree is dirty or the local branch sits on a
   different commit, printing both SHAs and what to do. `--force` overrides.
4. Apply the snapshot with `git stash apply`, restoring the staged, unstaged,
   and untracked split from the stash-format parents.

**Divergence**, rather than a stored claim: the same branch dirty on more than
one machine is detected directly from fleet state and surfaced in `status`.
This needs no write coordination — a grabbing machine writes only its own hub
directory — and catches divergence however it arises, not only after a grab.
A claim record written on grab remains a possible refinement if an explicit
"this was taken by laptop" marker proves useful.

## MCP server

`poppy mcp` (stdio transport), tools:

| Tool | Purpose |
|------|---------|
| `fleet_status` | machines, heartbeats, per-machine dirty/unpushed counts |
| `dirty_trees` | every dirty tree fleet-wide, sorted by age |
| `find_work(query)` | locate branches/repos matching a name or keyword |
| `repo_state(machine, repo)` | full state doc |
| `snapshot_history(machine, repo, branch)` | time-machine listing |
| `where_was_i(machine?)` | narrative digest of recent activity |

Read-only in v1 (`grab` stays human-initiated). Ships with a Claude Code
`.mcp.json` snippet.

## CLI surface

```
poppy status                     # fleet overview (live + last-known)
poppy dirty                      # all dirty trees, oldest first
poppy find <query>               # where is this branch/work?
poppy grab <machine>:<repo> [br] # teleport work here
poppy snapshot [repo]            # force a WIP snapshot now
poppy restore --as-of <time>     # time-machine restore into worktree
poppy machines                   # agents, versions, heartbeats
poppy log <repo>                 # state history from the hub
poppy daemon                     # run the agent (poppyd)
poppy mcp                        # MCP server on stdio
```

## Security

- All transport rides Tailscale; the tsnet API is reachable only inside the
  tailnet and further restricted by ACL to the owner's devices.
- Hub access is plain SSH with existing keys; the GitHub mirror is private.
- Hub pre-receive hook confines each machine to its own ref/state namespace.
- Gitignore is the secrets boundary for snapshots; `.poppy.toml` tightens it.

## Failure modes

| Failure | Behavior |
|---------|----------|
| Hub unreachable | agent queues state commits + snapshot refs locally, pushes on reconnect |
| Machine dies | hub retains last state + all snapshots; recovery = clone repo, fetch WIP ref, apply |
| Closet server dies | GitHub mirror has everything; re-point agents |
| Clock skew | per-machine `seq` counters order events; timestamps are display-only |
| Repo deleted/moved | its document disappears on the next scan, which rewrites the machine's directory wholesale |
| Working tree git cannot read | recorded as `broken` with a one-line cause rather than a wall of repeated git errors; an orphaned worktree (parent repo removed or pruned) is named as such, since its contents are invisible to every ordinary git command and count as stranded work |
| Concurrent dirty edits on two machines | divergence detected on grab and in status; surfaced, never auto-merged |

## Implementation

**Language:** Go — single static binary deploys to all three machines,
`tsnet` is native, good `os/exec` ergonomics for shelling out to git.
Shell out to `git` for all plumbing (stash-create semantics, fsmonitor)
rather than reimplementing via go-git; correctness over purity.

**Milestones:**

1. **Observe** *(implemented)* — `poppyd` scan + state push; `poppy status/dirty/find`
   reading the hub. Usable value immediately.
2. **Live** — tsnet API, heartbeats, live-with-fallback status.
3. **Time machine** *(implemented)* — WIP snapshots, retention,
   `poppy snapshot/timeline/restore`, and per-repo mirror sync.
4. **Teleport** *(implemented)* — `poppy grab` fetches a source machine's
   snapshot from the mirror and applies it, reproducing the exact staged,
   unstaged, and untracked split. A destination that is dirty or whose branch
   has diverged from the snapshot's base is refused with an explanation;
   `--force` overrides. Divergence — the same branch dirty on more than one
   machine — is detected from fleet state and surfaced in `status`, which is
   simpler and more robust than a stored claim record and needs no write
   coordination between machines. An explicit claim written on grab remains a
   possible refinement.
5. **MCP** — read-only server + Claude Code config.
6. **Polish** — TUI, `where_was_i` digests, shell-prompt claim warnings.

**Testing:** fixture repos built in temp dirs for state-collection unit
tests; an integration harness that spins up two "machines" (two checkouts +
a local bare hub) to round-trip snapshot → push → grab; golden tests for MCP
tool output. The grab divergence matrix (clean/dirty × ahead/behind/diverged)
gets exhaustive table-driven coverage.

## Open questions

- Snapshot cadence defaults (2 min debounce / 10 min cap) — tune after
  real-world use.
- Should `grab` clear the source machine's dirty tree after a successful
  claim (opt-in `--sweep`)? Deferred; advisory claims only for v1.
- TUI framework (bubbletea is the obvious choice) — decide at milestone 6.
