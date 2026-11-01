# `poppy status --json`

`poppy status --json` prints every machine's stored documents, with poppy's
own judgements added next to them. Read these fields instead of
re-implementing the rules: a rule copied into another tool drifts from
poppy's.

```json
{
  "Machines": [
    {
      "Manifest": { "machine": "mac", "heartbeat": "…", "hub": { … } },
      "Repos": [ { "name": "poppy", "branches": [ … ], "atRisk": true, "unpushed": 1 } ],
      "Sessions": [ … ],
      "Deploys": [ … ],
      "Live": true,
      "stale": false,
      "atRisk": 3,
      "unpushed": 1
    }
  ],
  "hubIntegrity": {
    "verdict": "clean",
    "lastRan": "2026-11-01T12:00:00Z",
    "ran": 2,
    "silent": 1,
    "reports": [ { "machine": "Mac.localdomain", "result": { … }, "stale": true } ]
  }
}
```

`Manifest`, `Repos`, `Sessions`, `Deploys` and `Live` are the documents poppy
stores on the hub, unchanged. The added fields are:

| Field | Where | Meaning |
| --- | --- | --- |
| `stale` | machine | The heartbeat is older than `staleAfterSeconds` (default one hour). Its numbers are the last ones it sent. |
| `atRisk` | machine | How many of its repositories are at risk. |
| `unpushed` | machine | How many of its repositories have at least one unpushed branch. |
| `atRisk` | repo | The repository holds work that exists only here: uncommitted changes, unpushed commits, stashes, no remote at all, or a working tree git cannot read. |
| `unpushed` | repo | How many branches have commits no remote has. A branch with no upstream counts. A repository with no remote reports 0, because `atRisk` already covers it. |

`hubIntegrity` is the fleet's view of the shared hub repository, the same
object the MCP `fleet_status` tool returns. `verdict` is one of `clean`,
`found`, `failed`, `skipped`, or `""` (no current check ran). Only `clean`
means a current check looked and found nothing. `ran` and `silent` count
machines that checked and machines that did not. See
[Hub integrity check](hub-check.md) for how the verdict is formed.

The MCP `fleet_status` tool reports the same machine counts, from the same
code.
