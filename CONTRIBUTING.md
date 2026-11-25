# Contributing

Issues and pull requests are welcome.

## Running the tests

hatch pins its own tools, so nothing needs installing beyond Go. `task` below is the pinned [Task](https://taskfile.dev), run as `go tool -modfile=tools/task.mod task`.

```sh
task hooks       # once per clone: points git at hooks/
task pre-commit  # format, vet and the lint of the Taskfile; a commit runs it
task pre-push    # adds the build for each system and the tests; a push runs it
```

`task --list` shows the rest.

## Pull requests

- One change to a pull request, with a test that fails without it.
- A commit message says what changed and why, in a short first line.
- Add a note for the change log with `changie new`; a change with no effect on users needs none.
- Keep to the code's style: `gofmt`, `go vet` and the lint in `.golangci.yml` are part of the tiers.
