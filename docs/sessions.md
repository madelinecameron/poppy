# Agent sessions

poppy records Claude Code and Codex sessions: which repositories and branches
each worked in, and the mailbox address that reaches it. Agents use this to find
each other, and branches show which sessions touched them.

## Hooks

Have the harness run `poppy session-hook <claude|codex>` on its lifecycle
events. The command reads the hook's JSON from stdin, prints nothing, and always
exits 0.

For Claude Code, in `~/.claude/settings.json`:

```json
{
  "hooks": {
    "SessionStart":     [{ "hooks": [{ "type": "command", "command": "poppy session-hook claude" }] }],
    "UserPromptSubmit": [{ "hooks": [{ "type": "command", "command": "poppy session-hook claude" }] }],
    "Stop":             [{ "hooks": [{ "type": "command", "command": "poppy session-hook claude" }] }],
    "SessionEnd":       [{ "hooks": [{ "type": "command", "command": "poppy session-hook claude" }] }]
  }
}
```

For Codex, run `poppy session-hook codex` on the same events.

`SessionEnd` is optional. It is the only event that marks a session ended.
Without it, a session is judged by when it was last seen.

## Commits

Each repository and branch entry also keeps `heads`: the commits HEAD pointed
at while the session was there, oldest first, at most 100. They tie a session
to the pull request that merged its work and the deploy that shipped it.

## Mailbox address

At `SessionStart` the hook asks the mailbox server for the session's address.
Set the server URL in `~/.config/poppy/config.json`:

```json
{ "mailbox": { "url": "http://mailbox-host:8080" } }
```

`MAILBOX_URL` overrides it. The token comes from `MAILBOX_TOKEN`, or else from
the `export MAILBOX_TOKEN='mbx_...'` line in `${ZDOTDIR:-$HOME/.zsh}/secrets.zsh`.
With no URL, sessions are recorded without an address.

## Reading

```sh
poppy sessions                         # every session, most recent first
poppy sessions --active                # seen in the last 30 minutes, not ended
poppy sessions --repo poppy --branch main
poppy sessions --active --within 2h
poppy sessions --repo poppy --json
```

The MCP server has the same view as the `sessions` tool.

## Where things are

| What | Where |
|------|-------|
| Local records | `~/.local/share/poppy/sessions/<session_id>.json` |
| Hook errors | `~/.local/share/poppy/session-hook.log` |
| Hub documents | `machines/<machine>/sessions/<session_id>.json` |

A new session or a branch switch reaches the hub within about 15 seconds while
the agent is running. The last-seen time is updated every scan. Sessions not
seen for 30 days are deleted.
