# Contributing

Thanks for helping. wasim is only useful if it behaves like the real API, so
accuracy matters more than feature count.

## Development

You need Go (the version in `go.mod`). There are no other build dependencies:
the SQLite driver is pure Go and the console has no build step. The browser
tests also need Node 24 and pnpm.

```sh
make test-race   # Go tests with the race detector
make lint        # golangci-lint v2
make e2e         # Playwright tests that drive the console in Chromium
make screenshots # regenerate the images in docs/images
make run         # start wasim on localhost
```

The first `make e2e` may ask you to install the browser with
`pnpm --dir e2e exec playwright install chromium`.

## Changing emulated behaviour

Every behaviour should trace back to a source. When you add or change one:

1. Cite Meta's documentation in the pull request. If you observed it against the
   real API, include the request and response with personal data removed.
2. If the docs and real traffic disagree, follow real traffic and record the
   disagreement in [`docs/errors.md`](docs/errors.md) or the relevant doc.
3. Update [`docs/compatibility.md`](docs/compatibility.md): move the item between
   emulated and not emulated, or add, change or remove a known drift.
4. Add a test that pins the wire format, ideally against a sample from the docs.

Never commit real phone numbers, tokens, secrets or message content. Use
fictional numbers such as `15550000001`.

## Tests

- Each package has unit tests next to its code. `internal/app` holds the
  integration tests that run the whole process against a fake consumer.
- Changes to the console UI need a test in `e2e/tests`. Use role and label
  locators, never CSS classes, so the tests also guard accessibility.
- Tests must pass with `-race` and must not depend on timing luck: poll for an
  outcome instead of sleeping a fixed time.

## Code style

- `gofmt` and `golangci-lint run` must pass.
- Every package has a package comment and every exported identifier has a doc
  comment that starts with its name. `internal/doccheck` fails the tests
  otherwise. Where it helps, add an `Example` function: it shows up in
  `go doc` and runs as a test.
- Comments explain why, not what. English only, everywhere.
- Keep `internal/cloudapi` free of I/O: it is the readable statement of the contract.
- `go.mod` stays small. Adding a dependency needs a good reason in the pull request.

## Pull requests

Keep them focused, describe the behaviour change, and include tests. Bug reports
are most useful with the request wasim received and what the real API answers.
Add a line to `CHANGELOG.md` under Unreleased.

## Releases

Maintainers tag `vMAJOR.MINOR.PATCH` on `main`. The release workflow runs the
tests, attaches binaries for Linux, macOS and Windows with checksums, and pushes
a multi-architecture image to `ghcr.io`.
