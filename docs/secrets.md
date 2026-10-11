# Secret scanning and hooks

Each scan looks for credentials in three places:

- the working tree
- stashes
- commits on local branches that no remote has

The last two exist on one disk only. A pre-commit hook never sees them, and no
other machine can.

```sh
poppy secrets                  # every finding, stashes and unpushed commits first
poppy secrets --origin stash   # only findings in stashes
poppy secrets --rule github-token
poppy secrets --machine laptop
poppy secrets --json
```

## What is recorded

A finding is a location: repository, path, rule, line, and the stash or commit
that holds it. The matched value is never stored, not even masked, because
state documents are shared between machines. The `Finding` type has no field
that could hold a value.

## Rules

The rules are few so that findings stay worth reading. Pass the rule ID to
`--rule`.

| Rule ID | Matches |
|---------|---------|
| `private-key` | Private key blocks |
| `aws-access-key-id` | AWS access key IDs |
| `aws-secret-access-key` | AWS secret access keys |
| `github-token` | GitHub tokens |
| `anthropic-api-key` | `sk-ant-` keys |
| `provider-api-key` | Other `sk-` API keys |
| `slack-token` | Slack tokens |
| `slack-webhook` | Slack webhook URLs |
| `stripe-secret-key` | Live Stripe secret keys |
| `env-secret-assignment` | `*KEY`, `*TOKEN`, `*SECRET`, or `*PASSWORD` set in a `.env` file |

Template files (`.env.example`, `.env.sample`, `.env.template`, `.env.dist`) are
exempt from `env-secret-assignment` only. A real token in one is still reported.

Files matched by `.gitignore` are not scanned.

## Limits

Each repository's scan is bounded by file size, file count, commit count, and
time (see `secrets.*` in [configuration](configuration.md)). When a limit is
hit, the report says so. An empty report means nothing was found, not that the
scan ran out of budget.

## gitleaks

Set `"secrets": { "gitleaks": true }` to also run gitleaks over each working
tree. It is off by default so that machines with and without gitleaks installed
report the same results. poppy keeps only the rule, file, and line from its
output.

## Pre-commit hook

```sh
poppy hooks verify            # list repositories without the hook
poppy hooks install           # install it in every scanned repository
poppy hooks install --force   # also replace hooks you wrote yourself
poppy hooks uninstall
```

`install` does not overwrite an existing hook. It reports the repository and
leaves it alone. With `--force` it saves the old hook as
`pre-commit.poppy-backup` and restores it on `uninstall`.

If poppy is not on `PATH`, the hook lets the commit through and prints a
warning, so uninstalling poppy does not block commits.
