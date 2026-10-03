# Snapshots, restore, and grab

Each scan saves every dirty working tree as a git commit kept outside your
branches. A bad `checkout`, `reset`, `clean`, or rebase cannot lose that work.
Saving a snapshot never touches HEAD, the index, or the working tree.

## Restore

```sh
poppy snapshot                           # save this repository now
poppy timeline                           # list saved snapshots
poppy restore                            # restore the newest snapshot
poppy restore --as-of 2h                 # restore what existed two hours ago
poppy restore --as-of "2026-07-19 16:00"
```

`--as-of` takes a duration (`30m`, `2h`, `3d`) or a date and time.

Restore will not apply over a working tree that has its own uncommitted
changes, because the two sets of changes could not be separated afterwards.
`--force` overrides this.

Snapshots use the same commit layout as `git stash`, so staged, unstaged, and
untracked changes come back exactly as they were. `git stash apply <sha>` works
on a snapshot without poppy.

### What is not saved

- Files matched by `.gitignore`. Keep secrets such as `.env` ignored and they
  stay on the machine.
- Untracked files larger than `snapshot.maxFileBytes` (10 MB by default). These
  are skipped and listed in the report.

Old snapshots are pruned: every snapshot is kept for 7 days, then one per day
for 90 days. See `snapshot.keepAllHours` and `snapshot.keepDailyHours` in
[configuration](configuration.md).

## Off-machine backup

With a mirror configured, each scan pushes new snapshots to a bare repository
on another machine, one per project:

```json
"snapshot": {
  "enabled": true,
  "mirror": { "enabled": true, "host": "hub-host", "dir": "/srv/poppy-mirrors" }
}
```

A snapshot carries the history it is based on, so a machine that has never
cloned the project can still recover the work. Leave `host` empty to mirror to
a local directory.

## Grab

Grab moves uncommitted work from another machine onto this one:

```sh
poppy grab laptop:api            # the latest snapshot of `api` from laptop
poppy grab laptop:api feature-x  # a specific branch
```

You need a local clone of the same project. Grab fetches the snapshot from the
mirror and applies it with staged, unstaged, and untracked changes intact.

It refuses to apply over local changes, or onto a branch that has moved away
from the snapshot's base, and says what it found. `--force` overrides both.

After a grab the same work exists on two machines. `poppy status` warns about
any project with uncommitted changes on the same branch in more than one place.
