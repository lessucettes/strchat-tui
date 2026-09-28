# strchat-tui

A small keyboard-driven terminal client for **public Nostr chats**.

- **Geohash channels:** kind `20000`, tag `g`; compatible with BitChat's public
  message format when both clients use the same geohash and a shared relay.
- **Named channels:** kind `23333`, tag `d`; also used by Nymchat. These are not
  BitChat location channels.
- **Local groups:** combine joined chats into a view; not a separate group protocol.

Messages are public, not encrypted. Ephemeral event kinds are not expected to be
stored by relays, but this is **not a deletion or privacy guarantee**.

## Run

Download a binary from [Releases](https://github.com/lessucettes/strchat-tui/releases)
or build with Go 1.25 or newer:

```sh
go build -trimpath -o strchat-tui ./cmd/strchat-tui
./strchat-tui --version
```

Release assets (no C compiler or runtime dependency needed):

| Target | Asset |
|---|---|
| Linux | `strchat-tui-linux` (amd64) |
| Android / Termux | `strchat-tui-android-arm64` |
| macOS Apple silicon | `strchat-tui-macos-arm` |
| macOS Intel | `strchat-tui-macos-intel` |
| Windows | `strchat-tui.exe` (amd64) |

Mark the downloaded file executable on Linux, macOS and Android (`chmod +x`).
`--version` (or `-v`) prints the version and, when the release build stamped it,
the commit and build date. No cross-compilation caveats apply: the client is pure
Go and builds with `CGO_ENABLED=0`.

No account registration, database or background service is required. Configuration
uses the OS user-config directory under `strchat-tui/config.json` (on Linux,
`$XDG_CONFIG_HOME/strchat-tui`, or `~/.config/strchat-tui`). It includes a private
key: do not publish it. Chat keys are separate ephemeral process-session keys;
local group replies retain the existing main-key behavior.

## Start chatting

```text
/join ucfv0
/join lobby
/nick alice
/relay
/help
/quit
```

A valid geohash is interpreted as a location channel. Other names are topic
channels. Only the active chat or local group is subscribed. Use the chat list or
`/set <name>` to switch. Geohash spelling and precision must match the other client
exactly; joining a parent cell does not subscribe to its children.

Useful commands:

| Command | Purpose |
|---|---|
| `/join <name> [name...]` | Join chats |
| `/set <name>` | Activate a chat/group |
| `/set <name1> <name2>...` | Combine existing chats in a local group |
| `/list`, `/del [name]` | List or leave chats |
| `/nick [name]` | Set or clear a nickname |
| `/relay [url...]` | List or add explicit relays |
| `/relay <number>` | Remove a configured relay |
| `/pow <0..16>` | Set per-chat proof-of-work; default 0, above the cap is rejected |
| `/block`, `/unblock` | Manage blocked participants |
| `/filter`, `/mute` | Include or hide matching text/regex |
| `/help` | Full command reference and aliases |

Use `@nick#suffix` completion to reply to a known participant; replies from local
combined views go to that participant's chat. This is still a **public** message,
not a DM.

## Predictable networking

- At most 12 selected relay connections; one subscription per relay.
- Explicit relays plus the nearest five geographic relays of **each** BitChat
  catalog (the app's reviewed list and upstream georelays); defaults for named
  chats when no relays are configured. Empty configuration does not mean offline
  mode.
- No recursive relay discovery, trial publications or background relay probing.
- Georelay selection works offline from two pinned bundled catalogs only.
  Startup and chat switching never download a relay list, and no local file can
  change it; refreshing a catalog means upgrading the binary.
- Reconnect continues while a relay remains selected, with jittered backoff;
  changing views updates subscriptions, and leaving stops unused connections.
- Bounded message queues, wire sizes, duplicate caches and terminal scrollback.
- A positive relay `OK` means acceptance by that relay, **not** delivery to a
  recipient. Missing acknowledgement is reported as unconfirmed; no automatic
  resend or durable outbox. Offline sends are rejected visibly, not silently kept.
- Chat session keys survive view switches, but rotate on process restart.

`ws://` supports local/private deployments; prefer `wss://` on untrusted networks.
Relay URL paths and query strings are preserved. Private URLs containing embedded
credentials are rejected.

### BitChat limits

The two current BitChat implementations use different geographic relay catalogs,
so a single-catalog client can share no relay at all with a peer of the other
variant (measured: 0/5 overlap for some geohashes). This client therefore bundles
both catalogs and subscribes to the nearest relays of each, which is what makes
those peers reachable. Configure an actual shared relay with `/relay <url>` when a
peer is in a chat where neither nearest set is reachable. The client keeps
live-first subscriptions, a small reconnect overlap, and local size/age limits; it
does not fetch BitChat's one-hour history or send presence heartbeats.
Keep `/pow 0` to avoid filtering peers that do not mine proof-of-work.

Presence, persistent per-geohash identities, bounded history and a richer
PoW policy are **proposals, not silently enabled features** (dual-catalog
selection was approved and is implemented). Generic NIP-17 DMs, NIP-29,
NIP-28, NIP-EE and Marmot/MLS are outside the project scope.

## Verification and architecture

```sh
go test ./...
go test -race ./...
go vet ./...
go test ./internal/client ./internal/tui -run '^$' -bench . -benchmem
python3 scripts/smoke_pty.py ./strchat-tui   # real terminal, no public relay
```

The Mage targets remain available and cover the released set
(`go run github.com/magefile/mage linux`, `macos`, `windows`, `android`, or
`all`). Cross-compilation requires no C compiler:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./cmd/strchat-tui
CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build ./cmd/strchat-tui
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./cmd/strchat-tui
```

Measured behaviour of this client is reported, not assumed: a 1000-message burst
costs **2 screen draws instead of 2007**, startup is ~15 ms to the first frame on
the development host, and a ten-minute soak over real sockets holds goroutines,
file descriptors and heap flat. The benchmark, spray and soak harnesses behind
those numbers are in `internal/client/*_test.go`, `internal/tui/bench_test.go`
and `scripts/`.

## License

MIT; see [LICENSE](LICENSE). The bundled public georelay data is pinned and
attributed in `internal/client/georelays.go`.