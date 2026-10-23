# strchat-tui

A keyboard-driven terminal client for **public Nostr chats**. Join geographic
or named channels, combine chats in local groups, and connect directly or
through SOCKS5/Tor.

Compatible with **BitChat** public geohash chats and **Nymchat** named chats
when clients use the same channel and share a relay.

**Messages are public and unencrypted.** Do not send secrets. Ephemeral events
are not a guarantee that a relay or another participant will delete a message.

![strchat-tui switching rooms and themes](assets/demo.gif)

## Installation

Download the binary for your platform from
[GitHub Releases](https://github.com/lessucettes/strchat-tui/releases/latest).

| Target | Asset |
|---|---|
| Linux amd64 | `strchat-tui-linux` |
| Android / Termux arm64 | `strchat-tui-android-arm64` |
| macOS Apple silicon | `strchat-tui-macos-arm` |
| macOS Intel | `strchat-tui-macos-intel` |
| Windows amd64 | `strchat-tui.exe` |

On Linux, download `SHA256SUMS` alongside the binary, then verify and run it:

```sh
sha256sum --ignore-missing -c SHA256SUMS &&
chmod +x strchat-tui-linux &&
./strchat-tui-linux
```

On macOS and Android, also mark the downloaded file executable with `chmod +x`.
Use `--version` (or `-v`) to print the version. To build from source, see
[Development](#development).

## Quick start

Enter commands in the Input pane:

```text
/nick alice
/join lobby
```

Type a message and press Enter to send it to the active chat. Use Chats or
`/set <name>` to switch chats; only the active chat or group is subscribed.

`/join ucfv0` joins a geographic channel because `ucfv0` is a valid geohash;
`/join lobby` joins a named channel. Geographic peers must use the exact same
geohash and precision. Joining a parent cell does not subscribe to its children.

Useful commands:

| Command | Purpose |
|---|---|
| `/join <name> [name...]` | Join chats |
| `/set <name>` | Activate a chat/group |
| `/set <name1> <name2>...` | Create and activate a local group of joined chats |
| `/list`, `/del [name]` | List or leave chats |
| `/nick [name]` | Set or clear a nickname |
| `/theme [name\|number]`, `/t` | List themes or switch and save colors |
| `/relay [url...]` | List or add explicit relays |
| `/relay <number>` | Remove a configured relay |
| `/pow <0..16>` | Set proof-of-work for the active chat/group; default 0 |
| `/block`, `/unblock` | Manage blocked participants |
| `/filter`, `/mute` | Include or hide matching text/regex |
| `/help` | Full command reference and aliases |

Use nickname completion to address a known participant with `@nick#suffix`.
In a group, a leading mention sends to that participant's chat instead of the
selected member. This is a **public reply, not a DM**. Completion uses full
public keys when short identifiers collide; ambiguous recipients are rejected.

## Keyboard controls

| Key | Action |
|---|---|
| `Ctrl+D` | Quit from any pane (`/quit` also works) |
| `Tab` / `Shift+Tab` | Cycle between visible panes |
| `Alt+I` | Focus Input |
| `Alt+O` | Focus Messages |
| `Alt+C` | Focus Chats |
| `Alt+N` | Focus Info; open it temporarily on small terminals |
| `Alt+L` | Focus Logs |
| `Ctrl+P` / `Ctrl+N` in Input | Recall older/newer messages and commands |
| Up/Down in Chats | Move through chats and groups |
| Enter in Chats | Activate the highlighted chat/group, or create a group from marked chats |
| Space in Chats | Mark/unmark a chat for a group |
| Delete in Chats | Leave the highlighted chat or remove the group |
| Up/Down in Messages or Logs | Scroll |
| `` ` `` in Messages or Logs | Maximize/restore the pane when space permits |

Input history holds the last 100 submitted lines for the current run only.
Moving forward past the newest entry restores your unfinished draft; history
is not saved to disk. Pane titles and the footer show contextual shortcuts.

### Mouse

Mouse-capable terminals support left-clicking a pane to focus it, clicking a
chat/group in Chats to activate it, and clicking a member of the active group
in Info to choose the sending destination. Click in Input to position the
cursor. The wheel scrolls the pane under the pointer without moving keyboard
focus; moving the pointer alone does nothing.

In tmux, mouse events are available when its `mouse` option is enabled. The
client does not change tmux or system settings. For terminal text selection,
use your terminal's mouse override (usually Shift while dragging).

## Local groups

A group combines joined geographic and named chats in one chronological feed.
It is a local view, not a new room: Enter sends to **one selected member**, not
to every chat.

For example, join two chats and combine them:

```text
/join lobby coffee
/set lobby coffee
```

You can also mark chats with Space in Chats and press Enter to create a group.
To choose where to send:

1. Highlight the active group in Chats and press `Alt+N` to focus Info.
2. Use Up/Down to choose a member; Home/End select the first/last. The `▶` marker
   shows the destination immediately, without an extra confirmation.
3. Press `Alt+I` to return to Input, then type and send with Enter.

The first member is selected by default. Each group's choice is saved across
restarts while that member remains in the group; removing it selects the first
remaining member. Deleting the group removes its saved choice. A save error is
reported if the new choice cannot be written to disk.

The `●` thread markers identify messages from the selected chat. Switching the
destination does not hide other chats, change subscriptions or move scrollback.
A leading `@nick#suffix` overrides the destination for that message without
changing the selection. Unknown or changed destinations are refused rather
than silently rerouted.

On small terminals, `Alt+N` temporarily replaces the feed with Info; `Alt+I`
returns to the feed and input without losing the draft.

## Filter and mute

- `/filter <pattern>` keeps only messages matching at least one enabled filter.
  With no enabled filters, all messages pass this check.
- `/mute <pattern>` hides messages matching any enabled mute rule, even if they
  also match a filter. **Filter selects what to include; mute selects what to exclude.**

Both match message content using a case-sensitive literal substring or a
[Go regular expression](https://pkg.go.dev/regexp/syntax) wrapped in `/.../`.
For example, `/filter bitcoin` keeps messages containing `bitcoin`, while
`/mute /spam|scam/` hides messages containing `spam` or `scam`.

The same regexp has opposite effects. With no other rules, used separately:

| Command | Effect |
|---|---|
| `/filter /\p{Latin}/` | Show only messages containing a Latin letter, including `é` and `ñ`. |
| `/mute /\p{Latin}/` | Hide every message containing a Latin letter. |

For example, `Привет` is hidden by this filter but passes this mute; `hello`
and `Привет, world` pass the filter but are hidden by the mute. URLs containing
Latin letters match too. `/mute /[A-Za-z]/` matches only ASCII Latin letters,
unlike `\p{Latin}`. A mute hides the entire message, not just the matching
characters or one line.

Use `/filter` or `/mute` without arguments to list rules, or with a rule number
to toggle it. `/unfilter <number>` and `/unmute <number>` remove a rule; omit
the number to clear the corresponding list. Rules are saved in the configuration
and affect newly received messages, not retained scrollback.

## Themes

`/theme` lists the available themes and marks the current selection:

1. `default` — the original green-on-black theme (used when no theme is saved).
2. `monochrome` — high-contrast black and white.
3. `blue-gray` — cool blue accents on a dark gray background.
4. `red-gold` — red borders and gold accents on a dark warm background.

Use `/theme blue-gray`, `/theme red-gold`, or a number such as `/t 4`.
The choice is saved and restored at the next launch. Switching themes recolors
the interface and retained messages without resetting input, history or scrollback.

## SOCKS5 and Tor

With a local Tor SOCKS listener running on port 9050, start the client with:

```sh
./strchat-tui-linux --socks=127.0.0.1:9050
```

Substitute your platform's binary name if different. The `--socks` option also
accepts `socks5://host:port` and `socks5h://host:port`. Pass it on every launch;
it is not saved in the configuration. Proxy authentication is not supported.

All relay connections use the proxy, including geographic relays, reserves,
redirects and loopback destinations. Relay hostnames, including `.onion`, are
resolved by the proxy. Use an IP address for the proxy itself to avoid local
resolution of its hostname. If the proxy fails, there is **no direct fallback**.

Add an onion relay with the usual command, replacing the placeholder with its
actual address:

```text
/relay ws://<relay-address>.onion
```

Use `ws://` for an onion service offering plain WebSocket on port 80, or `wss://`
if it offers TLS with a trusted certificate. TLS verification stays enabled.
Tor changes transport routing; public content, nicknames and traffic patterns
can still be linked, so it is not an anonymity guarantee.

## Relays and compatibility

- Named chats use configured relays, or bundled defaults when none are set.
  Failed configured relays do not trigger a switch to unrelated public defaults.
- Geographic chats use the nearest five relays from each of two bundled BitChat
  catalogs, in addition to configured relays. The catalogs are included in the
  binary; the client does not download relay lists at startup.
- If a geographic relay fails, the client can temporarily try the next five
  relays from that catalog while continuing to retry the primary set. A working
  primary is not replaced just because a reserve is faster.
- Connection attempts and retries are paced. A shared limit of 256 relay
  connections applies to the active chat/group; a warning reports reduced
  coverage if a large group or relay list exceeds it.

Use `/relay` to inspect configured relays and `/relay <url>` to add one.
Prefer `wss://` on untrusted networks; `ws://` is available for local or onion
deployments. Relay URLs containing credentials are rejected.

Geographic messages use kind `20000` with a `g` tag, compatible with BitChat's
public message format when both clients use the same geohash and share a relay.
Named messages use kind `23333` with a `d` tag, also used by Nymchat; these are
not BitChat location channels. Reserve relays cannot guarantee overlap with a
peer that only listens to its nearest set. If peers cannot see each other,
check the exact channel and configure a shared relay on both clients.
Keep `/pow 0` when communicating with peers that do not mine proof-of-work.

The client is live-first: it does not fetch BitChat's one-hour history or send
presence heartbeats. Relay acceptance is not proof of recipient delivery.
Sends without an acknowledgement are reported as unconfirmed and are not
resent after a timeout or disconnect. There is no persistent outbox.

### Relay authentication (NIP-42)

Authentication is automatic when a relay sends an `AUTH` challenge. The client
signs kind `22242` proofs with the session keys of the chats subscribed on that
connection; it does not create a separate application identity or save keys.
The relay receives those session public keys even if you have not posted yet.
Proofs are sent as `AUTH`, never as chat messages.

After an `auth-required:` rejection, the client waits for the matching `AUTH`
acknowledgement and retries the subscription or the **same signed event** once.
It also handles the `auth` hint in `EOSE`. Challenges and authentication results
are connection-local: reconnecting requires a fresh exchange, and switching
chats authenticates newly needed keys. Waiting is bounded; denied access is
reported, and subscription/authentication failures use a paced retry cooldown.
To limit relay-driven signing and bandwidth amplification, challenges are
limited to 1024 bytes and four new values per minute on each connection.

Multiple chats share one socket per relay. This relies on NIP-42's requirement
that relays retain all authenticated keys; relays that remember only the last
key may reject group traffic. Authentication proves key ownership, not a right
to access a relay: `restricted:` can still mean payment or allowlist membership
is required. Ephemeral chat keys do not provide membership tied to a permanent
key. Authentication does not encrypt messages or prevent a relay from linking
the chats on its connection.

## Configuration and privacy

Configuration is saved in the OS user-config directory under
`strchat-tui/config.json`. On Linux this is
`$XDG_CONFIG_HOME/strchat-tui/config.json`, or
`~/.config/strchat-tui/config.json` when XDG_CONFIG_HOME is unset.

The file stores joined chats/groups, selected group destinations, relays,
nickname, theme, filters and mutes, but no private keys. These settings can
still reveal personal information, including joined geographic channels.

Each chat has an independent signing identity kept only in memory. Sending to
the same chat separately or through any local group uses the same identity.
Groups have no signing identity of their own. Restarting the client, or leaving
and rejoining a chat, creates a new identity. There is no global or persistent
sender key.

Without a custom nickname, each chat's nickname is derived from its session
public key. `/nick` sets a shared display-name preference without changing
keys; clearing it restores the individual chat nicknames. Reusing a nickname
can link activity across chats. Relays can also associate chats carried over
the same connection; separate keys alone do not guarantee anonymity.

## Development

Go 1.27 or newer is required. Build and check the project from the repository root:

```sh
go build -trimpath -o strchat-tui ./cmd/strchat-tui
./strchat-tui --version
go test ./...
go test -race ./...
go vet ./...
```

Optional benchmarks and loopback relay-scale check:

```sh
go test ./internal/client ./internal/tui -run '^$' -bench . -benchmem
STRCHAT_RELAY_SCALE=1 go test ./internal/client -run '^TestRelayGroupSafetyCeilingLoopback$' -count=1 -timeout=90s -v
```

### Release builds

Mage is pinned in `go.mod`; no separate installation is needed. List targets
or build all release binaries:

```sh
go run github.com/magefile/mage -l
go run github.com/magefile/mage
```

The default target builds the five assets listed above into the repository
root with `CGO_ENABLED=0`; no C compiler is required. For individual targets:

```sh
go run github.com/magefile/mage linux
go run github.com/magefile/mage android
go run github.com/magefile/mage windows
go run github.com/magefile/mage macOS
```

`macOS` builds both macOS architectures. The individual `linux` and `android`
targets write `strchat-tui`; use the default target for release asset names.

## License

MIT; see [LICENSE](LICENSE). The bundled public georelay data is pinned and
attributed in `internal/client/georelays.go`.