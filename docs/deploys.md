# Deploys

poppy carries houseparty's deploy records to the hub, so a reader on any
machine sees deploys run from every machine. poppy never runs or inspects a
deploy itself.

## How a deploy arrives

After each deploy, `houseparty deploy` pipes its record to
`poppy deploy-record`. The command checks the record names an id, a time and
the project, env and service, then writes it to
`~/.local/share/poppy/deploys/<id>.json`. The agent publishes it within about
15 seconds, or at the next scan.

The record is houseparty's: who deployed, from which machine, whether it
worked, how long it took, and the service's source repo, branch and the
revision it ran afterwards.

## Reading

```sh
poppy deploys                        # last 30 days, newest first
poppy deploys --since 168h --failed
poppy deploys --project workplace --json
poppy deploys --env prod --service mailbox --limit 5
```

`poppy status --json` carries the same records under each machine's `Deploys`.
The MCP `deploys` tool gives agents the same list with the same filters.

## Where things are

| What | Where |
|------|-------|
| Local records | `~/.local/share/poppy/deploys/<id>.json` |
| Hub documents | `machines/<machine>/deploys/<id>.json` |

Deploys older than 90 days are deleted.
