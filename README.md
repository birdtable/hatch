<p align="center"><img src="docs/mark/hatch-light.svg" alt="hatch" width="96"></p>

# hatch

One small command for the checks that Go projects share.

<p>
  <a href="https://github.com/birdtable/hatch/releases"><img src="https://img.shields.io/github/release/birdtable/hatch.svg" alt="Latest release"></a>
  <a href="https://pkg.go.dev/github.com/birdtable/hatch"><img src="https://pkg.go.dev/badge/github.com/birdtable/hatch.svg" alt="Go reference"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue.svg" alt="License: Apache-2.0"></a>
</p>

hatch judges a test run, a tier's time, a git history and a Go module the same way on every machine. You tell it what counts as red, and it holds that line. It assumes nothing about your project, so one pinned copy serves them all.

## Install

```sh
go install github.com/birdtable/hatch/cmd/hatch@latest
```

Or pin it in your project's own tool module and call it from there:

```sh
go tool -modfile=tools/hatch.mod hatch <command>
```

## Quick start

List the packages of your module, without the ones you leave out:

```sh
hatch tests packages 'github.com/birdtable/hatch/internal/...'
```

```
github.com/birdtable/hatch/cmd/hatch
```

Then run them. You say which counts make the run red and which words end the verdict line:

```sh
hatch tests --timeout 10m --red-when failed,none-ran --green-word ship --red-word hold ./...
```

```
hatch tests: <n> passed, 0 failed, 0 skipped: ship
```

The first command is also a Go example test in this repository, so it is checked on every run.

## What's inside

| Command | What it does |
|---|---|
| `hatch tests` | Runs `go test` and judges the counts by the rule you name. A run that hangs is killed, tree and all. |
| `hatch budget` | Holds a tier to its time. An overrun is noted, not failed, when the machine was loaded. |
| `hatch secrets` | Runs your pinned gitleaks over the history and is green only when it scanned a commit and found nothing. |
| `hatch names` | Checks a hashed list of forbidden names against the tree, the branch and the text you give it. |
| `hatch runs` | Finds which package starts which outside program, and judges that against a rules file. |
| `hatch age` | Refuses a Go module version younger than the window you set. |
| `hatch writes` | Holds a command to writing nothing under a root, and lists what it wrote. |
| `hatch tag` | Holds a tree to what a release built from it must be, before the tag and after. |

Every flag, exit code and rule is in the [command reference](docs/commands.md). Run a command with no flag to see its usage.

## Built on

hatch asks little of your module. Its direct requirements are:

- [go-proc](https://github.com/birdtable/go-proc) starts every program hatch runs, in a tree it can kill, and reads the load.
- [go-files](https://github.com/birdtable/go-files) lists a tree by handle, without following a link, and reads a trusted file.
- [go-testkit/taskfile](https://github.com/birdtable/go-testkit) and [go.yaml.in/yaml/v3](https://pkg.go.dev/go.yaml.in/yaml/v3) read the Taskfile in hatch's own tests.
- [golang.org/x/mod](https://pkg.go.dev/golang.org/x/mod) tells a pseudo-version from a release for `hatch age`.
- [gotest.tools/gotestsum](https://pkg.go.dev/gotest.tools/gotestsum) reads the events of `go test -json` for `hatch tests`.

## Systems

Linux, macOS and Windows, on amd64 and arm64. On Windows there is no load average, so `hatch budget` reports the load as unknown and judges a tier strictly.

## Stability

v0: the API may change between minor versions.

## Contributing

Issues and pull requests are welcome. [CONTRIBUTING.md](CONTRIBUTING.md) says how to run the tests.

## Security

Please report a vulnerability privately. [SECURITY.md](SECURITY.md) says how.

## License

Apache-2.0. See [LICENSE](LICENSE).
