# Contributing

Bug reports and pull requests are welcome. tinwire is small on purpose: one Go binary, no
daemon, nothing listening on the network. Please keep it that way.

## Development

You need Go 1.24+ and, for end-to-end testing, a Mac set up as in the README.

```sh
make            # build ./tinwire
make test       # go vet + race-enabled unit tests
make smoke      # drive the real TUI in a pseudo-terminal (dry-run; needs a configured host)
TINWIRE_DEMO=1 go test -run TestDemoScreen ./ui   # print the README screen with made-up data
```

Layout and design decisions are in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). Read its
"Testing" section before writing pseudo-terminal tests: the TUI queries the terminal at startup
and a harness has to answer, or the first input bytes are swallowed.

## Ground rules

- **Never print message content** in logs, `--check`, `--debug-log`, tests or bug reports. Use
  counts, lengths and made-up data.
- **No SIP-off features.** Anything that needs imsg's IMCore bridge stays out.
- **Treat sender data as hostile**: filenames, paths and text come from other people.
- Sends are never retried automatically when delivery is uncertain.
- Add a test for every bug fix. The `ui` package tests run the model without a terminal; see
  `fixture()` in `ui/model_test.go`.

## Releasing

Tag `vX.Y.Z` and push the tag. The release workflow builds linux/amd64, linux/arm64 and
darwin/arm64 archives with a `SHA256SUMS` file and publishes a GitHub release.
