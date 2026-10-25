# Agent Instructions

Go 1.27+ terminal client for public Nostr chats. Keep README a short project
showcase with builds and a small example, not a manual. Command reference lives
in the in-app `/help`; this file contains only essential development guidance.

## Source map

- `cmd/strchat-tui/main.go`: flags, client/UI wiring and shutdown.
- `internal/client/client.go`, `types.go`: owner loop and action/event contracts.
  `state.go`, `config.go`, `identity.go`: chats, groups, settings and session keys.
- `internal/client/nostr.go`: receive validation, recipient previews and sends.
  `relays.go`, `relay_selection.go`, `relay_plan.go`: transport lifecycle/planning;
  `ws_gorilla.go`, `proxy.go`, `resolver.go`, `relay_auth.go`: WebSocket, DNS, AUTH.
- `internal/tui`: input/commands, locked model and event-loop rendering.
  `scrollback.go`/`frame_screen.go` handle bounded rendering; `conversation.go`
  handles group destinations; `clear.go`/`input_field.go` handle clearing.
- `magefiles/magefile.go`: release targets. `third_party/secp256k1-voi/UPSTREAM.md`:
  local crypto fork, provenance and update procedure.

## Ownership and shutdown

- `client.Run` owns config, identities, caches and relay handles. Workers receive
  immutable jobs/snapshots; capture destination, key, nickname, PoW and relays
  before queueing a send. UI state snapshots must not alias mutable config.
- Client events update the locked TUI model; only the tview event loop mutates
  widgets. Repaints are coalesced with nonblocking wake events, not blocking
  tview queue calls after shutdown.
- Start transport before potentially blocking display writes. `main` closes
  events only after client `Run` returns; never close actions while UI can send.
  Cancel and join owned workers/readers/probes. Ping must not block frame draining.
  TUI construction redirects the standard logger; idle UI tests must restore it.

## Critical behavior

- Messages are public/unencrypted: geohash chats use kind `20000`/`g`, named chats
  `23333`/`d`. Compatibility needs an exact channel and shared relay; a parent
  geohash does not subscribe to children.
- Keys are per-chat and memory-only, shared across views; groups have no key.
  Resolve the destination before signing. Preview/send share `resolveMessageTarget`;
  reject stale contexts and ambiguous recipients rather than silently rerouting.
- Groups show all members but send to one selected member, never broadcast.
  A leading mention overrides that message's destination. Selection persists;
  keyboard/mouse/commands share `groupFocus`. Validate `/set <group> <member>`
  before mutation; destination-only changes must not resubscribe or move history.
- `/clear` must be fast and silent. Clear Messages, input/undo and participant
  caches, **not Logs**, config, identities, destination, dedup IDs or transport.
  Preserve the nonblocking CLEAR/CLEARED boundary and new local feedback. Do not
  force GC or restart networking. `SetText` retains undo: keep the stable editor
  wrapper and normalize its inner editor's mouse focus/capture.
- Filter is inclusion, mute is exclusion and wins; patterns are case-sensitive
  literals or `/Go regexps/`. Apply to new messages, not retained scrollback.
- `INFO` events go to Messages; `STATUS` goes to Logs as INFO. WARN is degradation,
  ERROR a failed operation. Log meaningful transitions/results, not repeated
  retries; sanitize relay text. Unconfirmed sends must not be silently resent.
- Explicit relay failures must not enable public defaults. Geographic catalogs
  are embedded/pinned; changes alter peer rendezvous. Keep planner invalidation,
  bounded queues/caches and Unicode/cell correctness when optimizing.
- SOCKS must cover all relays/redirects with remote DNS, fail closed and retain TLS
  verification. Never persist proxy settings or signing keys. NIP-42 uses existing
  session keys and socket-local challenges; do not invent a permanent identity.
- Verification's variable-time crypto tables are public-data-only, never for
  secret signing material. Follow the fork's `UPSTREAM.md` for crypto changes.

## Build and checks

Use a full checkout: `go.mod` has a local crypto replacement, so version-suffixed
`go install ...@version` is unsupported. Regular builds need no C compiler;
`purego` selects portable verification. Race checks need a supported CGO toolchain.

```sh
CGO_ENABLED=0 go build -tags purego -trimpath -o strchat-tui ./cmd/strchat-tui
go test ./...
CGO_ENABLED=0 go test -tags purego ./...
go test -race ./...
go vet ./...
```

Mage is pinned: `go run github.com/magefile/mage -l` lists targets; no target (or
`all`) builds all release assets with `purego`/CGO disabled. `linux`/`android` write
`strchat-tui`; `macOS` builds both architectures; `windows` writes `strchat-tui.exe`.
Root binaries are ignored. Crypto changes also need tests inside the nested fork.

Optional: benchmarks use `go test -tags purego ./internal/client ./internal/tui
-run '^$' -bench . -benchmem` (one command). Scale/soak tests require
`STRCHAT_RELAY_SCALE=1` / `STRCHAT_SOAK_DURATION`; inspect their tests for usage.
The gitignored `scripts/smoke_pty.py` is optional local tooling, not a fresh-clone
prerequisite; it uses isolated config and a dead loopback relay.

## Change discipline

- Keep the existing client/TUI split. Read relevant definitions/callers before edits.
- Prefer a few valuable behavioral tests using existing fixtures, not a new
  `_test.go` per feature or checks for already-covered implementation details.
- Tests must isolate config and use explicit loopback relays; cancel/join workers.
  Never expose or overwrite real OS user config (`strchat-tui/config.json`).
- Preserve bounded history, scroll/draft/focus behavior and pane shortcuts in UI work.
  Keep artifacts in English and wrap underlying errors with `%w` when adding context.
