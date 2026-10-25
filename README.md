# strchat-tui

A keyboard-driven terminal client for **public Nostr chats**. Join geographic
or named channels, combine them in local groups, and connect directly or
through SOCKS5/Tor.

Compatible with **BitChat** public geohash chats and **Nymchat** named chats
when clients use the same channel and share a relay.

![strchat-tui switching rooms and themes](assets/demo.gif)

- Local groups bring several chats into one feed; each message goes to one chat.
- Public replies with nickname completion, filters, mute rules and themes.
- Automatic relay reconnection, geographic reserves and NIP-42 authentication.
- Runs on Linux, Windows, macOS and Android/Termux.

**Messages are public and unencrypted.** Session keys and Tor do not guarantee
anonymity, and ephemeral events do not guarantee deletion by relays or peers.

## Install

Download a binary and `SHA256SUMS` from
[GitHub Releases](https://github.com/lessucettes/strchat-tui/releases/latest).
For Linux:

```sh
sha256sum --ignore-missing -c SHA256SUMS &&
chmod +x strchat-tui-linux &&
./strchat-tui-linux
```

## Build

Requires **Go 1.27+**, with no C compiler. Build from a full repository checkout,
including the local dependency in `third_party`:

```sh
git clone https://github.com/lessucettes/strchat-tui.git
cd strchat-tui
CGO_ENABLED=0 go build -tags purego -trimpath -o strchat-tui ./cmd/strchat-tui
./strchat-tui
```

Mage is pinned in `go.mod`; no separate installation is needed:

```sh
go run github.com/magefile/mage -l       # list targets
go run github.com/magefile/mage          # build all release binaries
go run github.com/magefile/mage linux    # or android, windows, macOS
```

The default target produces Linux amd64, Windows amd64, macOS Intel/Apple
silicon and Android arm64 binaries in the repository root. Individual `linux`
and `android` targets write `strchat-tui`; `macOS` builds both architectures.
All Mage builds use `CGO_ENABLED=0` and the `purego` tag. `--version` shows build
provenance when stamped by Mage.

## Start chatting

Enter commands in the Input pane:

```text
/nick alice
/join lobby coffee
/set lobby coffee
```

This joins two named chats and combines them in a local group. Enter sends to
one selected member, never broadcasts; choose the destination in Info (`Alt+N`).
A leading `@nick#suffix` makes a public reply, not a DM. `/join ucfv0` joins a
geographic chat instead. Use `/help` or `/help chats` for commands; pane titles
and the footer show keyboard shortcuts. `Ctrl+D` quits.

With a local Tor SOCKS listener:

```sh
./strchat-tui --socks=127.0.0.1:9050
```

Relay DNS goes through the proxy, with no direct fallback if it fails.
The proxy option is not saved; pass it on each launch.

## Technical notes

- Geographic chats use Nostr kind `20000` with a `g` tag; named chats use kind
  `23333` with a `d` tag. Geographic peers need the exact same geohash and
  precision, plus a shared relay. Relay catalogs are bundled, not fetched at startup.
- Each chat has its own in-memory signing key, shared by all local views of that
  chat. Groups have no sender key; restarting creates new identities.
- Settings and joined chats are saved in the OS user-config directory under
  `strchat-tui/config.json`. Private keys and message history are not saved.
- The client is live-first, without a persistent outbox. A relay acknowledgement
  means acceptance by that relay, not proof of delivery to another participant.

## License

MIT; see [LICENSE](LICENSE). Bundled relay data is attributed in
`internal/client/georelays.go`; the local crypto dependency retains its own license.