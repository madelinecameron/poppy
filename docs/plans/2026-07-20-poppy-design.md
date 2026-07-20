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
machines/<hostname>/repos/<repo-id>.json   # one state doc per repo
```

`repo-id` = short hash of the canonicalized first remote URL (so the same
project is correlated across machines regardless of local path), or of the
absolute path for remoteless repos.

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

Snapshots chain: each new snapshot's ref history is preserved by writing to
`refs/poppy/wip/<machine>/<branch>` and keeping prior commits reachable via a
per-branch reflog-style ref (`refs/poppy/wiplog/<machine>/<branch>`), giving
`poppy restore --as-of "yesterday 4pm"`.

**Privacy:** gitignored files are never snapshotted (`.env` and friends stay
put). A per-repo `.poppy.toml` can exclude additional patterns or opt the
repo out entirely.

**Retention:** hub-side prune job — keep every snapshot for 7 days, then last
per day for 90 days, then drop; `git gc` after prune. Agents prune local refs
to match.

**Push:** each machine force-pushes only its own namespace
(`refs/poppy/*/<machine>/*`), enforced by a hub pre-receive hook.

## `poppy grab`

`poppy grab <machine>:<repo> [branch]` — teleport work from another machine.

1. Resolve repo/branch; query the source machine live via tsnet. If online
   and dirty, ask it to take a final snapshot now; else use the hub's latest.
2. Fetch `refs/poppy/wip/<machine>/<branch>` from the hub.
3. Locally: check out the branch (creating it at the snapshot's parent if
   absent). Refuse if the local tree is dirty or the local branch has
   diverged from the snapshot's parent — print exactly why and what to do.
4. Apply the snapshot as working state, restoring the staged/unstaged/
   untracked split from the stash-format parents.
5. Write a **claim** record to the hub (`machines/<src>/repos/<id>.json`
   gains `claim: {by, at, snapshotSha}`) and notify the source agent over
   tsnet. The source machine's `poppyd` marks the tree "claimed elsewhere";
   any new edits there trigger loud warnings (agent status, shell-prompt
   hook, `poppy status` banner). Claims are advisory, not locks — divergence
   is detected and surfaced, never silently merged.

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
| Repo deleted/moved | tombstone record in state doc, pruned after 30 days |
| Concurrent dirty edits on two machines | divergence detected on grab and in status; surfaced, never auto-merged |

## Implementation

**Language:** Go — single static binary deploys to all three machines,
`tsnet` is native, good `os/exec` ergonomics for shelling out to git.
Shell out to `git` for all plumbing (stash-create semantics, fsmonitor)
rather than reimplementing via go-git; correctness over purity.

**Milestones:**

1. **Observe** — `poppyd` scan + state push; `poppy status/dirty/find`
   reading the hub. Usable value immediately.
2. **Live** — tsnet API, heartbeats, live-with-fallback status.
3. **Time machine** — WIP snapshots, retention, `poppy restore`.
4. **Teleport** — `poppy grab`, claims, divergence guards.
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
