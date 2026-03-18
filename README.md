# DPE CORP — THE TERMINAL CHAT REVOLUTION

**Brought to you by DPE Corp.** Fork of strchat-tui. *Believe me — this is not your grandfather’s chat app.*

---

Look, people are saying — and they’re smart people, the best people — that this is **maybe the greatest** terminal Nostr client ever built. We’re talking **fast**. We’re talking **beautiful**. We’re talking **keyboard-driven power** like you’ve never seen. No bloat. No nonsense. **Huge** functionality. **Tremendous** relays. **America-first** engineering (runs on Linux, macOS, and Windows — we love all of them).

A simple, stylish TUI for Nostr-based chats. **DPE Corp** doesn’t settle for average. We deliver.

![DPE Corp terminal chat in action](./assets/image.png)

## What is this thing? (Spoiler: it’s incredible)

**strchat-tui** (powered by **DPE Corp**) is a terminal Nostr chat client. Lightweight. Fast. Highly functional. You get real chat rooms, real speed, real retro hacker vibes — **from the comfort of your terminal**. Other clients? Fine. This one? **On another level.**

Smart relay management keeps you connected. **Nobody** wants a slow relay. We find the good ones. We use the good ones. **That’s how winning is done.**

## Features — and they’re ALL winners

* **Terminal UI:** Clean. Keyboard-driven. Runs **anywhere**. *Beautiful.*
* **Smart Relay Management (the best):**
    * Measures latency — **only the fastest** relays make the cut.
    * Drops unstable relays. **Failing relays?** You’re fired.
    * Publishes to the fastest path — messages fly. **Believe me.**
* **Multi-Protocol Support:** Built for many Nostr chat `kinds`. **Big league.**
* **Geohash Chats:** Ephemeral local vibes via `georelay`. **Huge.**
* **Stylish Theme:** Hacker aesthetic. Readable. **Classy.**

On join or chat switch, the client asks relays for messages from the **last 10 minutes** (up to 2000 events per room). Override in `config.json`: `"history_lookback_minutes": 15` (max 1440).

## Supported Chat Types

**DPE Corp** supports a **tremendous** range of Nostr chats — and we’re not stopping.

#### ✅ Supported right now 

* **Kind 23333:** Public, ephemeral, topic groups (`#nym`, `#nostr`, you name it).
* **Kind 20000:** Geohash chats — local conversation. **Amazing.**

#### 🔜 Coming soon — will be HUGE

* **NIP-17:** Encrypted DMs.
* **NIP-28:** Public Chat.
* **NIP-A0:** Voice messages.
* **NIP-C7:** Chats.
* **NIP-EE:** E2EE with MLS. **The best encryption. Maybe ever.**

## How to Run

1. Go to [**Releases**](https://github.com/lessucettes/strchat-tui/releases).
2. Download the binary for **your** OS — Linux, macOS, or Windows.
3. Run it. **So easy.** So good.

## How to Build from Source

1. Install [Go](https://go.dev/) (1.25+). **Strong recommendation.**
2. Clone:
   ```bash
   git clone https://github.com/lessucettes/strchat-tui.git
   cd strchat-tui
   ```

### Option 1 — Go build (direct)

```bash
go build ./cmd/strchat-tui
```

### Option 2 — Mage (cross-platform, reproducible)

[Mage](https://magefile.org) builds for everyone. **Everyone loves Mage.**

```bash
go install github.com/magefile/mage@latest
```

| Command              | Description                                           |
|----------------------|-------------------------------------------------------|
| `mage` or `mage all` | Build all platforms (Linux, macOS, Windows)          |
| `mage linux`         | Linux / amd64                                         |
| `mage macintel`      | macOS / amd64                                         |
| `mage macarm`        | macOS / arm64                                         |
| `mage macos`         | macOS (amd64 + arm64)                                 |
| `mage windows`       | Windows / amd64                                       |

Then:

```bash
./strchat-tui
```

(Windows: `strchat-tui.exe`)

---

**DPE Corp** — *making terminals great again.*

## License

MIT. See `LICENSE`. **Very fair. Very legal.**
