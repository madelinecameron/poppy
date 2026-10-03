# MCP server

`poppy mcp` serves fleet state over the Model Context Protocol, so an AI agent
on any machine can answer questions about all of them. The server is read-only.

| Tool | Returns |
|------|---------|
| `fleet_status` | Every machine, when it last reported, and its dirty, unpushed, and backed-up counts |
| `dirty_trees` | Uncommitted work across all machines, oldest first |
| `find_work` | Where a repository or branch exists |
| `repo_state` | The full state of one repository |
| `snapshot_history` | Saved snapshots of a repository |
| `secret_findings` | Credential locations, including in stashes and unpushed commits |
| `where_was_i` | A summary of recent activity across machines, or on one |

## Setup

For Claude Code:

```sh
claude mcp add poppy -- poppy mcp
```

For any client that reads `.mcp.json`:

```json
{
  "mcpServers": {
    "poppy": { "command": "poppy", "args": ["mcp"] }
  }
}
```

Each tool call fetches the latest state from the hub first. If the hub is
unreachable, the tool answers from the last state this machine has.
