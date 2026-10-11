# Hub integrity check

Each scan also checks the shared state repository itself, on the machine that
holds it. The result appears in `poppy status` and in every machine's report.

Git can keep running on a damaged repository. A restart in the middle of a push
can leave zero-length object files behind. Git warns once, skips the bad pack,
and every command still succeeds, so a check that only asks git sees nothing
wrong. poppy runs two checks against the files directly:

- a search for zero-length files under `objects/`
- `git fsck --full --strict`, for damage that leaves a file non-empty

```sh
poppy hub-check                      # check the hub this machine publishes to
poppy hub-check --json
poppy hub-check --no-fsck            # zero-length search only
poppy hub-check --local --repo PATH  # check a repository on this machine
```

## Results

The result is `clean` only when both checks ran and found nothing. If a check
could not run (host unreachable, nothing configured, check turned off, output
cut short), poppy reports `NOT CHECKED` with the reason instead of `clean`.

`poppy hub-check` exits non-zero for any result other than `clean`.

## Access

The check runs shell commands on the hub machine, so it cannot use the account
in `hubRemote` if that account's shell is `git-shell`. It connects with
`hubCheck.host`, or `snapshot.mirror.host` if that is unset, and takes the
repository path from `hubRemote`. It only reads. If no host is set, the check
reports itself as not configured.

When poppy runs on the hub machine itself, both checks run locally without
ssh.

## Cost

A full `git fsck --strict` of a 136 MB hub takes about 16 seconds. It runs
every cycle by default. Set `hubCheck.fsckIntervalSeconds` to run it less
often. A cycle that skips the fsck is reported as not clean. A restart of the
hub machine since the last check always triggers a full fsck.
