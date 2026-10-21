# strchat-tui

A small keyboard-driven terminal client for **public Nostr chats**.

- **Geohash channels:** kind `20000`, tag `g`; compatible with BitChat's public
  message format when both clients use the same geohash and a shared relay.
- **Named channels:** kind `23333`, tag `d`; also used by Nymchat. These are not
  BitChat location channels.
- **Local groups:** combine joined chats into a view; not a separate group protocol.

Messages are public, not encrypted. Ephemeral event kinds are not expected to be
stored by relays, but this is **not a deletion or privacy guarantee**.

![strchat-tui switching rooms and themes](assets/demo.gif)

Local demo: `lobby`, `coffee`, and `devchat` with all four themes. Messages come
from a loopback-only demo relay, not public chats.

## Run

Download a binary from [Releases](https://github.com/lessucettes/strchat-tui/releases)
or build with Go 1.27 or newer:

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
| `/theme [name\|number]`, `/t` | List themes or switch and save colors |
| `/relay [url...]` | List or add explicit relays |
| `/relay <number>` | Remove a configured relay |
| `/pow <0..16>` | Set per-chat proof-of-work; default 0, above the cap is rejected |
| `/block`, `/unblock` | Manage blocked participants |
| `/filter`, `/mute` | Include or hide matching text/regex |
| `/help` | Full command reference and aliases |

Use `@nick#suffix` completion to reply to a known participant; replies from local
combined views go to that participant's chat. This is still a **public** message,
not a DM.

When nicknames and short key suffixes collide, completion uses full public keys.
Ambiguous replies and block/unblock requests are rejected instead of choosing an
arbitrary participant.

In the input field, `Ctrl+P` recalls older messages and commands; `Ctrl+N`
moves forward and restores your unfinished draft after the newest entry.
The last 100 non-empty submitted lines are kept in memory for this run only,
including send attempts that failed; history is never written to disk.

Press `Ctrl+D` to quit from any pane (or use `/quit`). `Ctrl+C` no longer exits.
Rejected input can ring the terminal bell (BEL), for example at a history
boundary, at the input length limit, or when typing into a read-only pane.
Feedback is rate-limited; normal navigation and background relay errors are silent.

### Mute messages by content

`/mute` hides incoming messages whose content matches an enabled mute rule.
Wrap a [Go regular expression](https://pkg.go.dev/regexp/syntax) in `/.../`;
without these delimiters, the rule is a literal substring, not a regexp.
For example, hide any message containing a Latin letter, including accented
letters such as `é` and `ñ`:

```text
/mute /\p{Latin}/
```

The regexp matches anywhere in the message, so mixed Cyrillic/Latin text and
URLs containing Latin letters are hidden too. It hides the entire message,
not just the matching characters or one line of a multiline message. To match
only ASCII Latin letters, use `/mute /[A-Za-z]/` instead.

Use `/mute` to list rules and their numbers, `/mute <number>` to toggle a rule,
`/unmute <number>` to remove it, or `/unmute` to clear all rules. Rules are saved
in the configuration and affect newly received messages, not retained scrollback.

### Themes

`/theme` lists the available themes and marks the current selection:

1. `default` — the original green-on-black theme (used when no theme is saved).
2. `monochrome` — high-contrast black and white.
3. `blue-gray` — cool blue accents on a dark gray background.
4. `red-gold` — red borders and gold accents on a dark warm background.

Use `/theme blue-gray`, `/theme red-gold`, or a number such as `/t 4`.
Colors change without restarting, including retained messages and logs; input,
history, focus and chat selection are preserved. The selected name is saved in
the `theme` field of `config.json` and restored at the next launch. Old configs
without this field (or with an unknown theme name) use `default`. If saving
fails, an error is shown and the previous theme remains active.

## Predictable networking

- The active chat/group selects its working relay set, with a safety ceiling of
  256 workers for the whole view, not per chat. Each worker has one socket and
  subscription. At most four connection attempts run concurrently, with starts
  paced at least 100 ms apart; waiting is cancelable.
- Explicit relays plus the nearest five geographic relays of **each** BitChat
  catalog (the app's reviewed list and upstream georelays); defaults for named
  chats when no relays are configured. Empty configuration does not mean offline
  mode.
- Combined views share the connection budget round-robin by relay rank, rather
  than letting the first chat consume it. Geographic rendezvous sets precede extra
  explicit anchors, so a long manual list cannot crowd out BitChat primaries.
  All primary pools precede reserves. When the safety cap excludes
  relays, a warning reports reduced coverage; use fewer explicit relays, smaller
  groups, or shared relays. This cap is a resource guard, not a protocol limit.
- A failed geographic primary can temporarily use the next five nearest
  addresses of its own catalog. Working primaries are never displaced by a
  reserve. A replacement must add a distinct route for that chat; an address
  already in its primary pool or chosen reserve is not counted again.
  No full-catalog scan, recursive discovery, trial publication or
  inactive-chat probe is performed; an exhausted reserve is reported visibly.
- Georelay selection works offline from two pinned bundled catalogs only.
  Startup and chat switching never download a relay list, and no local file can
  change it; refreshing a catalog means upgrading the binary.
- Failed addresses leave the working set while cooling down. Retry windows grow
  from 500 ms to five minutes with jitter, and reset only after one minute of
  stable service. A bounded, in-memory per-URL cache preserves cooldowns across
  view switches; nothing is saved to disk. Named chats with explicit relays never
  switch to unrelated public defaults because those relays failed.
- Geographic primary recovery attempts continue even when reserves are working.
  After a primary is stable for one minute, an unnecessary replacement is
  removed, unless another active chat still needs it. Shared addresses use one
  connection. Changing views updates subscriptions; leaving stops unused sockets
  and retries. Rejection of one publication does not mark a relay unavailable.
- Failover subscriptions request at most 30 seconds of recent overlap, including
  when another chat is attached to an existing reserve connection. Relays need
  not retain ephemeral events, so this is not a delivery/history guarantee.
- Bounded message queues, wire sizes, duplicate caches and terminal scrollback.
- A positive relay `OK` means acceptance by that relay, **not** delivery to a
  recipient. Missing acknowledgement is reported as unconfirmed; no automatic
  resend or durable outbox. Offline sends are rejected visibly, not silently kept.
- Chat session keys survive view switches, but rotate on process restart.

`ws://` supports local/private deployments; prefer `wss://` on untrusted networks.
Relay URL paths and query strings are preserved. Private URLs containing embedded
credentials are rejected.
Malformed configured relay URLs stop startup with an error rather than silently
disabling networking or falling back to public defaults. The file is left intact.

### BitChat limits

The two BitChat source variants referenced by the bundled snapshots use different
geographic relay catalogs, so a single-catalog client can share no relay at all
with a peer of the other
variant (measured: 0/5 overlap for some geohashes). This client therefore bundles
both catalogs and subscribes to the nearest relays of each, which is what makes
those peers reachable when a common primary is available. A reachable reserve
does not guarantee overlap with a peer that only listens to its nearest five;
it supplements, rather than permanently replaces, primary recovery. Configure
an actual shared relay with `/relay <url>` when a peer is in a chat where neither
nearest set is reachable. The client keeps
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
STRCHAT_RELAY_SCALE=1 go test ./internal/client -run '^TestRelayGroupSafetyCeilingLoopback$' -count=1 -timeout=90s -v
```

### Build release binaries with Mage

Mage v1.17.2 is pinned in `go.mod`; no separate Mage installation is needed.
From the repository root, list targets or build the full release set with the
default target (`all`):

```sh
go run github.com/magefile/mage -l
go run github.com/magefile/mage
```

The default target builds all five binaries into the repository root:
`strchat-tui-linux`, `strchat-tui-android-arm64`, `strchat-tui-macos-arm`,
`strchat-tui-macos-intel`, and `strchat-tui.exe`. To build selected targets,
append one of these case-sensitive Mage target names:

```sh
go run github.com/magefile/mage linux
go run github.com/magefile/mage android
go run github.com/magefile/mage windows
go run github.com/magefile/mage macOS
```

`macOS` builds both macOS architectures. Mage sets `CGO_ENABLED=0` for
cross-compilation, so no C compiler is required.

Measured behaviour of this client is reported, not assumed: a 1000-message burst
costs **2 screen draws instead of 2007**, startup is ~15 ms to the first frame on
the development host, and a ten-minute soak over real sockets holds goroutines,
file descriptors and heap flat. Rendering benchmarks and relay soak tests are
in `internal/tui/bench_test.go` and `internal/client/*_test.go`.

## License

MIT; see [LICENSE](LICENSE). The bundled public georelay data is pinned and
attributed in `internal/client/georelays.go`.