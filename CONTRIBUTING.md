# Contributing

Thanks for helping. Bug reports, fixes and documentation improvements are
welcome. For a larger change, open an issue first so we can agree on the
approach. Security problems go through [SECURITY.md](SECURITY.md), not
public issues.

## Build

You need Go (the version in `go.mod`; the `toolchain` line makes `go`
download it) and a C toolchain, because password checks use PAM through cgo:

- macOS: the Xcode command line tools (`xcode-select --install`).
- Linux: gcc and the PAM headers (`sudo apt install build-essential libpam0g-dev`
  on Debian and Ubuntu, `sudo dnf install gcc pam-devel` on Fedora).

```sh
make build     # bin/cravv-connect, bin/cravv-relay, bin/cravv-conformance
```

For the Cloudflare relay you need Node.js 22 or newer:

```sh
make relay-cf-test    # typecheck and the Vitest suite, no Cloudflare account needed
make conformance-cf   # the Go conformance suite against relay-cf under wrangler dev
```

## Test

```sh
gofmt -l .     # must print nothing
make vet
make test      # go test ./... -race -count=1
```

The tests never touch your real `~/.claude`, `~/.cravv-connect`, Keychain or
login services: they use temporary homes and fakes. Please keep it that way
in new tests.

Some tests check the documentation against the code, and they fail a change
that forgets the docs:

- `TestREADMEDocumentsEveryCommand`: every CLI command and flag is in the
  README's CLI reference.
- `TestIPCDocMatchesRegistry`: every daemon IPC method is in
  `protocol/ipc-v1.md`, with the right gate.
- `TestDocsHaveNoEmDashes` and the UI copy tests: no em dashes in Markdown or
  user-facing text. Use a colon, a semicolon, parentheses or two sentences.
- `TestThirdPartyLicensesUpToDate`: after changing `go.mod`, regenerate
  `THIRD_PARTY_LICENSES` with `scripts/third_party_licenses.sh > THIRD_PARTY_LICENSES`.

A change to a wire format also updates its spec in `protocol/`, and a relay
change keeps both relays passing the conformance suite.

## Pull requests

- Keep a pull request to one change, with tests that fail without it.
- Match the surrounding code: small interfaces, the existing naming, comments
  that say why.
- Anything that widens what a peer, a relay or an agent can do needs a note
  in [docs/security.md](docs/security.md).
- Add a line to the "Unreleased" section of [CHANGELOG.md](CHANGELOG.md).

By contributing you agree that your contribution is licensed under the MIT
license in [LICENSE](LICENSE).
