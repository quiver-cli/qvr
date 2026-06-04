# Contributing to Quiver

Thanks for your interest. Quiver is in active development, and APIs or on-disk
formats may still shift before v1.0.

## Build & test

```bash
git clone https://github.com/raks097/quiver.git
cd quiver
make all          # fmt + lint + test + build
```

Requires Go 1.22+. `make all` runs `gofmt`, `golangci-lint run`,
`go test ./...`, and `go build`. CI runs the same checks on every PR.

Run the binary directly without installing:

```bash
go run . --help
go run . init my-skill
go run . validate testdata/valid-skill
```

## Filing issues

- **Bug reports** — include the `qvr --version`, your OS, and the exact
  command + minimal repro. Output of `qvr doctor` is helpful when the
  bug touches an installed skill.
- **Feature requests** — describe the workflow you're trying to support
  before the API you'd like. Quiver leans hard on git primitives; if a
  feature can be expressed as "use the existing git command for X,"
  that's usually the right answer.

## Pull requests

- Open an issue first for anything larger than a typo or a small bug fix
  so we can agree on shape before you write code.
- Keep PRs focused. One change per PR — easier to review, easier to
  revert.
- Tests required for any new behaviour. The codebase uses table-driven
  tests with `testify` and integration tests against local bare git
  repos (see `test/skill/integration_test.go` for the pattern).
- Run `make all` locally before pushing.

## Code style

- `gofmt` and `goimports` are enforced — `make fmt` will format.
- Wrap errors with `fmt.Errorf("context: %w", err)`.
- Keep interfaces in the consumer package, not the provider.
- Every command supports `--output json` — structured data to stdout,
  diagnostics to stderr.

## License

By submitting a PR, you agree that your contribution is licensed under
the MIT license (see `LICENSE`).
