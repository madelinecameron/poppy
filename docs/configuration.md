# Configuration

poppy reads `~/.config/poppy/config.json`. `poppy init` writes a starter file:

```json
{
  "machine": "laptop",
  "roots": ["~/code"],
  "hubRemote": "ssh://hub-host/~/poppy-hub.git",
  "scanIntervalSeconds": 300,
  "includeWorktrees": true
}
```

With `hubRemote` empty, poppy records state on this machine only and shares
nothing. When the hub is unreachable, the agent keeps state locally and
publishes it on the next successful cycle.

## Keys

| Key | Meaning |
|-----|---------|
| `machine` | Name of this machine in the fleet. Defaults to the hostname. |
| `roots` | Directories to scan. `~` is expanded. Default `["~/code"]`. |
| `hubRemote` | Git URL of the shared state repository. Empty means local only. |
| `hubDir` | A hub clone another poppy keeps on this machine. Set it and this configuration only reads. See [Reading another poppy's hub](#reading-another-poppys-hub). |
| `scanIntervalSeconds` | How often the agent scans. Default 300. |
| `skipDirs` | Extra directory names never scanned. |
| `maxDepth` | How deep below a root to look for repositories. Default 8. |
| `includeWorktrees` | Report linked worktrees as separate entries. |
| `staleAfterSeconds` | When a machine that has not reported counts as stale. Default 3600. |
| `snapshot.enabled` | Capture uncommitted work each scan. Default true. |
| `snapshot.maxFileBytes` | Largest untracked file to capture. Default 10 MB. |
| `snapshot.keepAllHours` | Keep every snapshot for this long. Default 168. |
| `snapshot.keepDailyHours` | Then keep one per day for this long. Default 2160. |
| `snapshot.excludeRepos` | Glob patterns of repositories never captured. |
| `snapshot.mirror.enabled` | Push snapshots to a mirror each scan. |
| `snapshot.mirror.host` | SSH target holding the mirrors. Empty means a local path. |
| `snapshot.mirror.dir` | Directory holding one bare mirror per repository. |
| `secrets.enabled` | Scan for credentials each cycle. Default true. |
| `secrets.gitleaks` | Also run gitleaks over each working tree, if installed. |
| `secrets.intervalSeconds` | How long a result is reused for an unchanged repository. Default 3600. |
| `secrets.maxFileBytes` | Largest file or object examined. Default 512 KB. |
| `secrets.maxWorktreeFiles` | Most working-tree files read per repository. Default 20000. |
| `secrets.maxUnpushedCommits` | Most unpushed commits read per repository. Default 500. |
| `secrets.maxBlobs` | Most stash and commit objects read per repository. Default 5000. |
| `secrets.maxFindings` | Most findings recorded per repository. Default 200. |
| `secrets.timeoutSeconds` | Time limit for one repository's scan. Default 60. |
| `secrets.excludeRepos` | Glob patterns of repositories never scanned. |
| `hubCheck.enabled` | Check the hub repository each cycle. Default true. |
| `hubCheck.host` | SSH target on the hub machine with a normal shell. Empty uses `snapshot.mirror.host`. |
| `hubCheck.repo` | Path of the hub repository on that machine. Empty derives it from `hubRemote`. |
| `hubCheck.fsckIntervalSeconds` | Shortest gap between full `git fsck` runs. Default 0, every cycle. |
| `hubCheck.timeoutSeconds` | Time limit for one check. Default 120. |
| `mailbox.url` | Mailbox server that session hooks ask for each session's address. `MAILBOX_URL` overrides it. Empty skips the lookup. See [Agent sessions](sessions.md). |

`node_modules`, `vendor`, `.venv`, `target`, and similar directories are always
skipped. A repository nested inside another is treated as a submodule or a
vendored copy and is not tracked on its own.

## Running the agent

```sh
poppy daemon --install    # start at login (launchd) or boot (systemd user unit)
poppy daemon --uninstall
poppy daemon              # run in the background without a service
poppy daemon --status
poppy daemon --stop
poppy scan                # scan once, now
```

The daemon picks up a new binary on its own. Between cycles it checks whether
the file it started from has been replaced, by a new file renamed over it or the
same file rewritten, and if so exits with status 75. The service manager starts
it again on the new binary: systemd (`Restart=always`, or `on-failure` in units
from older versions) or launchd (`KeepAlive`). A daemon started without a
service just stops; start it again.

On Linux, `--install` enables lingering so the user service starts without a
login.

## Configuring from a manifest

A fleet that already describes its hub and mirror in a
`workplace-protocols.yaml` file can configure poppy from it:

```sh
poppy init --workplace-protocols-file ./workplace-protocols.yaml
poppy init --from-workplace-protocols   # find the manifest over Tailscale
```

`--from-workplace-protocols` needs the `tailscale` CLI.

## Environment

`POPPY_CONFIG` overrides the config file path. `POPPY_DATA_DIR` overrides where
local state is kept. `POPPY_HUB_DIR` overrides `hubDir`.

## Reading another poppy's hub

A service that only reads the fleet, such as a status board in a container,
does not need its own clone of the hub. Point `hubDir` (or `POPPY_HUB_DIR`) at
the clone this machine's poppy daemon already keeps, usually
`~/.local/share/poppy/hub`, mounted read-only if the service is in a container.

- Reads use that clone's last commit, never its working tree. The daemon
  resets the tree on every cycle, so a read that walked it could mix two
  states.
- poppy writes nothing to the clone and never fetches. It is as current as the
  daemon's last scan, every `scanIntervalSeconds`.
- `scan`, the daemon and `machines retire` refuse to run. Publish from the
  poppy that keeps the clone.
- Git refuses a repository another account owns. poppy marks the clone safe on
  its own command line, so the service needs no git configuration for it.

```json
{ "machine": "workplace-prod-core", "hubDir": "/var/lib/poppy-hub" }
```
