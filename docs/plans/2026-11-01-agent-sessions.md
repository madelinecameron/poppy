# Agent sessions — Design

**Date:** 2026-11-01
**Status:** Implemented

Track Claude Code and Codex harness sessions across the fleet: which sessions
exist, which repositories and branches each worked in, and the mailbox address
that reaches each one.

## Problem

Agents on different machines cannot find each other. An agent about to work on
a branch has no way to learn that another session is already on it, or how to
send that session a message. And a branch records commits, not the sessions
that made them.

## Goals

1. Answer "which sessions are working on this repository or branch right now?"
   from any machine.
2. Give each answer a mailbox address, so the asking agent can make contact.
3. Keep a short history of where each session has been, branch switches
   included.

## Non-goals

- No coordination or locking. poppy reports; agents decide.
- No reading of transcripts. The path is recorded so a person or agent on that
  machine can open it.

## Design

### Capture: `poppy session-hook <claude|codex>`

The harness runs the command on its lifecycle events with a JSON object on
stdin. poppy reads `session_id`, `transcript_path`, `cwd`, `hook_event_name`,
and `source`, and ignores the rest. Only `session_id` is required.

On each event the hook:

1. Takes a per-session `flock`, so two events of one session never interleave a
   read-modify-write.
2. Resolves `cwd` with one git call, `rev-parse --show-toplevel
   --absolute-git-dir`, and reads the branch from `HEAD` in the git directory.
   `rev-parse --abbrev-ref HEAD` fails on a branch with no commits, which is
   where a new session often starts.
3. At `SessionStart` only, calls `GET <mailbox>/v1/whoami` with
   `Authorization: Bearer <token>` and `X-Mailbox-Session: <session_id>`, with a
   two-second limit, and records the returned `address`.
4. Updates the session record and writes it atomically (temp file and rename).
5. Touches a marker file when the change is worth publishing at once.

The hook has three rules, because the harness waits on it before each prompt:

- **Nothing on stdout or stderr.** Claude Code puts hook stdout into the model's
  context.
- **Always exit 0.** Failures go to `session-hook.log` in the state directory,
  which starts over past 1 MB.
- **No network except at `SessionStart`.** The whole run has a five-second
  limit, and reading stdin is bounded by it. A typical event takes about 11 ms.

The token comes from `MAILBOX_TOKEN`, or else from the `MAILBOX_TOKEN=` line in
`${ZDOTDIR:-$HOME/.zsh}/secrets.zsh`, because hooks may run without the user's
shell environment. It is never logged or stored. The mailbox URL comes from
`MAILBOX_URL`, or else `mailbox.url` in the config file. With neither set, the
lookup is skipped and the session is recorded without an address.

### The record

One file per session at `~/.local/share/poppy/sessions/<session_id>.json`
(`$POPPY_DATA_DIR/sessions` when set). The same shape, `model.Session`, is what
the hub stores:

| Field | Meaning |
|-------|---------|
| `harness` | `claude` or `codex` |
| `sessionId`, `transcriptPath` | From the payload |
| `mailbox` | The whoami address, kept when a later lookup fails |
| `firstSeen`, `lastSeen` | First and latest event |
| `ended` | Set only by `SessionEnd`; cleared by a later `SessionStart` |
| `lastEvent` | The latest event's name |
| `repos` | One entry per repository and branch, each with its own first and last seen |

A branch switch adds an entry. Returning to a branch updates the existing
entry. A session keeps at most 50 entries and drops the least recently seen.

**Ended.** `Stop` is not an end: Claude Code fires it after every turn. Only
`SessionEnd` sets `ended`. Without that event, a session is judged by
`lastSeen`, and "active" means not ended and seen within 30 minutes by default.

### Publish

The agent publishes records to `machines/<machine>/sessions/<session_id>.json`
in the hub, with `machine` filled in and each repository's `repoId` set. It
replaces the directory on each publish, so a pruned session disappears from the
fleet.

`repoId` is derived the same way scanned repositories get theirs: a path this
machine's scan already recorded takes that repository's id; any other path is
resolved with the same remote-based derivation (`collect.RepoID`) and cached.
The hook does not do this, so it stays at one git call.

Sessions publish in two ways:

- **Every scan** (default every five minutes), after the repository publish.
  This carries `lastSeen` forward and recovers from any earlier failed push.
- **Between scans**, when the hook touched the marker. The agent stats the
  marker every 15 seconds and publishes if it moved. The hook touches it only
  for a new session, a new repository or branch, a mailbox address, a new
  transcript path, or a change to `ended`. A later `lastSeen` alone waits for
  the scan, so an active session does not put a commit in the hub on every
  prompt.

So a new session or branch switch reaches the hub within about 15 seconds, plus
the time to push. `lastSeen` lags by up to one scan interval, which is why the
default active window (30 minutes) is much longer than the scan interval. A
between-scans publish that changes nothing pushes nothing.

Both paths run on the agent's one goroutine, so they never use the hub clone at
the same time.

### Prune

Each publish deletes local records not seen for 30 days (`sessions.PruneAfter`).
Since the hub directory is replaced from the local records, the hub forgets them
at the same time.

### Read

- `poppy sessions [--repo X] [--branch Y] [--machine M] [--active] [--within 30m]`
- MCP tool `sessions` with `repo`, `branch`, `machine`, `active`, and
  `activeMinutes` (default 30).

`repo` matches a repository name, ignoring case, or a `repoId`. When `repo` and
`branch` are both given, they must match the same entry. Results are sorted by
`lastSeen`, newest first.

## Decisions

- **The hook never touches the hub.** A fetch and push on every prompt would
  put network latency in front of the user. The agent already owns the hub
  clone.
- **A marker file, not a file watcher.** A stat every 15 seconds costs nothing
  and needs no extra dependency or platform-specific code.
- **One record type for local and published state.** The agent only adds
  `machine` and `repoId`.
- **Sessions are their own documents and their own publish**, not part of the
  manifest, so a session change does not wait for or rewrite a scan.
- **`SessionEnd` is the only end signal.** Calling a session ended because it
  went quiet would be a guess, and `lastSeen` already says it went quiet.
