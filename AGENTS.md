# Agent Instructions

`strchat-tui` is a Go 1.27+ keyboard-driven terminal client for public Nostr chats. The executable is in `cmd/strchat-tui`; `internal/client` owns chat state and relay transport, and `internal/tui` handles input, model collection, and rendering. Read `README.md` for user-facing protocol and behavior constraints.

## Dev environment

- Use Go 1.27 or newer (`go.mod` declares `go 1.27.0`). The project is pure Go; builds do not require a C compiler.
- No Makefile, CI workflow, or dedicated linter configuration is present. `magefiles/magefile.go` defines release build targets.
- Main code is under `cmd/strchat-tui` and `internal/{client,tui}`. Relay catalog CSVs embedded by `internal/client/georelays.go` live beside that package.

## Build, test, and checks

Run from the repository root:

```sh
go build -trimpath -o strchat-tui ./cmd/strchat-tui
go test ./...
go test -race ./...
go vet ./...
go test ./internal/client ./internal/tui -run '^$' -bench . -benchmem
python3 scripts/smoke_pty.py ./strchat-tui
```

Optional sustained relay soak (uses loopback test relays):

```sh
STRCHAT_SOAK_DURATION=2m go test -race ./internal/client/ -run TestSoakRelayTransportSteadyState -timeout 10m -v
```

The README also documents Mage release targets: `go run github.com/magefile/mage linux`, `macos`, `windows`, `android`, or `all`.

## Code and test conventions

- Keep application-specific code in the existing `internal/client` or `internal/tui` packages; the command package wires the two together.
- Client `Run` owns configuration, identity, caches, and relay handles. TUI `Run` owns its worker goroutines; preserve the channel and shutdown contracts documented beside those methods.
- Keep widget mutations on the tview event loop; client events are folded into the TUI model before rendering.
- Tests are colocated as `*_test.go`, use Go's standard `testing` package, and commonly use table cases with `t.Run`.
- Wrap underlying errors with `%w` when adding context; configuration and relay/user errors are generally surfaced as typed display events.

## Pitfalls

- Chat messages are public and not encrypted. The OS user-config directory contains `strchat-tui/config.json` with a private key; do not expose or overwrite a user's config while testing.
- `go test ./...` does not run the sustained soak unless `STRCHAT_SOAK_DURATION` is set. The soak uses local `httptest` relays; it does not need public relays.
- The PTY smoke test needs a built binary and a POSIX controlling PTY. It creates an isolated temporary config and uses only a dead loopback relay, so it does not contact public relays or publish messages.
- Geographic relay catalogs are pinned CSV files embedded into the binary, not runtime-fetched data. Treat changes to them as protocol/reachability changes, not generated-file edits.
- The root binary names produced by builds are listed in `.gitignore`; do not mistake those artifacts for source changes.
