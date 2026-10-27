# Message-processing plugin protocol

Plugins are optional executables written in any language. They can replace text,
suppress a message, or leave it unchanged. No interpreter is embedded in the
client. Only one-shot `exec` mode is currently supported.

## Launch options

Enable plugins explicitly for the current process:

```sh
./strchat-tui \
  --plugin-send=/absolute/path/to/translate-plugin \
  --plugin-receive=/absolute/path/to/presentation-plugin \
  --plugin-timeout=35s
```

- `--plugin-send`: executable for `on_send` (outgoing messages).
- `--plugin-receive`: executable for `on_receive` (incoming messages).
- `--plugin-timeout`: execution timeout for each enabled plugin. Default `10s`;
  must be positive and at most `2m`. Duration values such as `500ms` are accepted.

Each handler is optional and disabled unless its flag is supplied. Plugins and
their timeout are never read from or saved to `config.json`; a normal launch
without these flags enables no plugins. Single-dash forms such as `-plugin-send`
also work, just like `-socks`. Empty executable values, unavailable executables,
and invalid timeouts fail startup rather than silently disabling processing.

Each executable flag accepts one path or executable name, **not a shell command**.
The process is launched directly, without arguments or a shell. Quote paths
containing spaces in your launching shell. Prefer absolute paths; bare names use
the executable search path. There is no client-side expansion of `~`, pipes,
globs, or command strings.

The executable may be written in any language. On Unix, an interpreted script
needs an appropriate shebang and execute permission. If an interpreter or handler
needs arguments, use a launcher executable/script that supplies them; the client
does not parse command lines or infer interpreters from filename extensions.

Plugins inherit the client's environment and working directory. Keep API settings
and credentials in the plugin's own environment or private files, not in client
settings or command-line secrets.

## JSON protocol, version 1

For each message the client starts one process, writes one compact UTF-8 JSON
object followed by a newline to stdin, then closes stdin. The executable must
write exactly one JSON response to stdout and exit successfully. Use an ordinary
JSON encoder: newlines inside strings must be escaped. Reserve stdout for the
response; diagnostics belong on stderr. Additional stdout JSON objects or text
are errors. A trailing newline is recommended.

Outgoing request example:

```json
{"version":1,"plugin":"on_send","direction":"outgoing","chat":"lobby","content":"Привет!","sender":{"pubkey":"<64 lowercase hex characters>","nick":"alice"}}
```

Incoming request example:

```json
{"version":1,"plugin":"on_receive","direction":"incoming","chat":"lobby","content":"Hello!","sender":{"pubkey":"<64 lowercase hex characters>","nick":"bob"},"event_id":"<64 lowercase hex characters>","created_at":1793000000}
```

Fields:

- `version`: protocol version, currently `1`.
- `plugin`: handler name, `on_send` or `on_receive`.
- `direction`: `outgoing` or `incoming`.
- `chat`: the actual destination channel, never a local group name.
- `content`: original message text. Incoming text is from the validated signed
  event, before display sanitization/truncation.
- `sender.pubkey`: public Nostr key as lowercase hex. For outgoing requests this
  is the destination chat's captured session identity.
- `sender.nick`: captured outgoing nickname or sanitized incoming display name.
- `recipient`: optional public key from the resolved outgoing mention or first
  incoming `p` tag. This is public addressing metadata, not an encrypted DM.
- `event_id`, `created_at`: incoming-only signed event ID and Unix timestamp in
  seconds. Outgoing messages do not yet have a signed event ID or timestamp.

All metadata is read-only. **Private signing keys are never sent to plugins.**

Choose exactly one response:

```json
{"version":1,"action":"unchanged"}
```

```json
{"version":1,"action":"replace","content":"Hello!"}
```

```json
{"version":1,"action":"suppress"}
```

`replace` requires a string `content`; `unchanged` and `suppress` must omit it.
Unknown actions, unsupported versions, missing replacement text, invalid UTF-8,
and malformed JSON fail processing. Unknown fields are ignored for forward
compatibility; they cannot change identities, recipients, chats, tags or other
protocol metadata. Handlers should likewise ignore unknown request fields.

The objects are deliberately suitable for JSON Lines framing: a future
persistent mode can reuse the same messages and actions. Persistent processes,
plugin chains, and other extension capabilities are not implemented.

## Processing and failure behavior

- **Outgoing:** the owner loop first resolves and captures the destination,
  recipient, signing identity, nickname, PoW and relay targets. The send worker
  runs `on_send` before event signing, PoW and publication. Replacement text
  cannot reroute the message, even if it introduces a leading mention or the user
  changes chats while processing. Empty/whitespace-only text, invalid UTF-8, more
  than 2000 grapheme clusters, or more than 16 KiB is rejected after processing.
  Use `suppress`, rather than empty text, to intentionally cancel a send.
- **Incoming:** event admission, signature/ID verification, deduplication, and
  filter/mute checks happen before `on_receive`. Rules match the original text
  using the existing display sanitization and truncation. Rejected messages
  never enter the plugin queue or launch a process. Mute takes priority over
  filter. Only the local display text changes; the original signed event is
  never rewritten. The result is sanitized/truncated for display, but is not
  filtered again: a translation can change the language without disappearing.
  Rules are evaluated when the original message arrives; changes while processing
  apply to subsequent messages, not already-admitted jobs. Blocking by public key
  happens before processing and is checked again before display. This includes
  admitted echoes of your own messages.
  An empty incoming replacement leaves an empty message row; use `suppress` to
  hide the message entirely.
- **Outgoing failure:** start failure, unsuccessful exit, bad response, timeout or
  output overflow produces an error. Nothing is published. There is no fallback
  to the original message and no automatic retry. Intentional suppression is
  reported in Logs.
- **Incoming failure:** a warning is logged and the original text, which already
  passed filter/mute, is displayed. Relay connections are unaffected.
  Intentional incoming suppression is silent.
- **Bounded work:** one process per direction at a time (at most two total), in
  separate sequential workers. The outgoing queue retains its existing limit of
  eight jobs. Incoming processing has 32 queued jobs and 32 completed-result
  slots, plus the current job. A full outgoing queue rejects the send. A full
  incoming queue drops new messages locally with a warning on overload, rather
  than blocking relay I/O or launching extra processes. Dropped incoming IDs stay
  deduplicated; another relay will not replay them through the plugin.
- Each process has its configured execution timeout, starting when its worker
  launches it (queue waiting is separate). Outgoing mining/publication retain
  their separate ten-second deadline after processing. Each stdout/stderr stream
  is limited to 128 KiB. External output is never copied into failure logs.
- Shutdown cancels running processes and joins the workers; pipe draining has an
  additional bounded 100 ms wait. Pending incoming results cannot repopulate
  Messages or participant caches after `/clear`, and are discarded if their chat
  is no longer in the active view when processing finishes.
  `/clear` also discards queued incoming work without waiting for the process
  already running.

## Writing and testing a handler

1. Decode one JSON object from stdin and check its version and handler name.
2. Compute the desired text without changing metadata.
3. Encode one versioned response plus a newline, then exit zero.
4. On a processing failure, exit nonzero; never return `unchanged` as an implicit
   fallback for a failed outgoing transformation.
5. Exercise the executable on a saved request before enabling it. Include
   quotes, Unicode and embedded newlines in tests. Ensure stderr never contains
   API credentials or message bodies you do not intend to disclose.

A standard-library-only Go translation example, with GigaChat OAuth and
OpenAI-compatible chat completions, is in
[the translation example README](README.md).

## Trust and network privacy

Plugins execute as your OS user, **not in a sandbox**. Only configure trusted
executables. They inherit the process environment and can access files and the
network with your permissions. The protocol does not supply signing keys, but it
is not an OS security boundary. Incoming plugins receive original validated text
only after it has passed filter/mute checks.
Do not make a plugin launch background daemons;
it must manage and terminate any children it creates. The client cancels its
configured executable, not arbitrary detached descendant processes.

The client's `--socks` option protects relay traffic only. It does not route or
restrict an external executable's network requests. Configure proxy/TLS behavior
inside the plugin separately. A translation API receives your message text and
may retain it even if subsequent Nostr publication fails or is suppressed.
