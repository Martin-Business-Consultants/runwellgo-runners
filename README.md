# Runners for Runwell

Hands code and other heavy work to machines you control. A runner (`runwell-runner`, from the [runwellgo releases](https://github.com/Martin-Business-Consultants/runwellgo/releases), on a server or a laptop with git, gh and a coding agent's CLI) picks a job up, works in a clean checkout, streams its log back and finishes with a branch, a pull request and a summary; nothing reaches Runwell until a person approves it. Runwell itself never runs a shell command. The runner binary stays in the runwellgo repository (`cmd/runwell-runner`); [docs/runners.md](https://github.com/Martin-Business-Consultants/runwellgo/blob/main/docs/runners.md) there says how to set one up. Its tables are `runners_*`.

A [Runwell](https://github.com/Martin-Business-Consultants/runwellgo) plugin: key `runners`,
version 1.0.0. It's in the official Runwell binary; an owner adds it from
**Settings › Plugins › Add a plugin** and switches it on, which grants what it asks for.

## What it asks for

- `records:read`: reads clients, engagements, work and the other core records, as agents see them
- `records:write`: writes through the core's own rules (notes, work, commitments, events), recorded as whoever made the call
- `tables`: keeps its own SQL tables (its manifest's Schema)
- `live`: pushes events to people's open tabs
- `notify`: puts notifications in people's bells
- `tools`: runs the agent tools the person is offered, as them
- `machines`: issues tokens to machines that call its machine routes

The grants are a contract the host keeps: the plugin reaches Runwell only through the
`*plugin.API` it's handed, holding what an owner granted ([docs/plugins.md](https://github.com/Martin-Business-Consultants/runwellgo/blob/main/docs/plugins.md)).

## Its screens

Its own screens are Svelte, in [`ui/`](ui/): `ui/index.js` exports the plugin's `key`, its `routes` (each shown only while the plugin is on) and a `setup()` that adds its tabs and slots. They're part of the official Runwell app: runwellgo's `make plugin-ui` copies this folder into the app's `ui/src/plugins/runners/` (from the module `go list -m` finds), and the app is built with it. They import the app's shared pieces (`$lib/…`, shadcn components) and talk only to the plugin's own routes, `/api/x/runners/…`.

## Building Runwell with it

The official binary (runwellgo's `cmd/runwell`) already has it. A Runwell of your own is a
`main.go` that hands `runwell.Main` its plugins:

```go
package main

import (
	"os"

	runners "github.com/Martin-Business-Consultants/runwellgo-runners"
	"github.com/Martin-Business-Consultants/runwellgo/v3/runwell"
)

// version is set from the release tag: go build -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	os.Exit(runwell.Main(version, runners.Module()))
}
```

`go get github.com/Martin-Business-Consultants/runwellgo-runners@v1.0.0` adds it. Runwell's app (embedded in the core module) already
carries this plugin's screens.

## Developing

```sh
go vet ./... && go test ./...
golangci-lint run ./...   # v2.15.0, .golangci.yml
```

The tests run the plugin on a real database through `plugintest` (a host, a temporary
database and a stand-in for the core). To try a change in the app, check this repository out
beside runwellgo and add it to runwellgo's `go.work` (`go work use ../runwellgo-runners`): the build
and `make plugin-ui` then use your checkout.

This code moved here from runwellgo's `plugins/runners` and `ui/src/plugins/runners` with its
history (runwellgo 3.1.0).

## License

[FSL-1.1-MIT](LICENSE.md), like Runwell.
