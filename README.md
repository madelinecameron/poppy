# poppy

poppy tracks every git repository on every machine you use and shows you the
work that exists in only one place: uncommitted changes, unpushed commits, and
stashes. It also backs that work up off the machine, so you can recover it or
pick it up somewhere else.

## Requirements

- git and ssh on every machine
- Go 1.26 or later, to build
- An always-on machine the others can reach over SSH, to hold the shared state
  repository. [Tailscale](https://tailscale.com) is the simplest way to connect
  them.
- macOS or Linux. The background agent runs under launchd or systemd.
- Optional: [gitleaks](https://github.com/gitleaks/gitleaks), as a second
  secret scanner

## Install

```sh
go install github.com/madelinecameron/poppy/cmd/poppy@latest
```

## Setup

On the always-on machine, create the shared state repository once:

```sh
git init --bare -b state ~/poppy-hub.git
```

On each machine, point poppy at it and start the agent:

```sh
poppy init --hub ssh://hub-host/~/poppy-hub.git
poppy daemon --install
```

`poppy init` writes `~/.config/poppy/config.json`. By default the agent scans
`~/code` every five minutes. Leave out `--hub` to use poppy on a single machine.

## Usage

```sh
poppy status              # every machine and the work at risk on it
poppy dirty               # uncommitted work across all machines, oldest first
poppy find auth-refactor  # which machines have this branch or repository
poppy restore             # recover the last snapshot of this repository
poppy grab laptop:api     # bring uncommitted work from another machine here
poppy secrets             # credentials found in working trees, stashes, and unpushed commits
```

Run `poppy` with no arguments for the full command list.

## Documentation

- [Configuration](docs/configuration.md)
- [Snapshots, restore, and grab](docs/snapshots.md)
- [Secret scanning and hooks](docs/secrets.md)
- [Hub integrity check](docs/hub-check.md)
- [MCP server](docs/mcp.md)

## Development

```sh
go test ./...
```

Tests run against real git repositories in temporary directories.
