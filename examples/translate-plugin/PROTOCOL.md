# Message-processing plugin protocol

Version 1 uses one executable invocation per message. A plugin returns unchanged
text, replacement text or suppression.

## Launch options

```sh
./strchat-tui \
  --plugin-send=/absolute/path/to/send-plugin \
  --plugin-receive=/absolute/path/to/receive-plugin \
  --plugin-timeout=35s
```

- `--plugin-send`: executable for `on_send` (outgoing messages).
- `--plugin-receive`: executable for `on_receive` (incoming messages).
- `--plugin-timeout`: timeout per invocation; default `10s`, greater than zero
  and at most `2m`. Queue waiting does not count toward this timeout.

Handlers are disabled when their flags are omitted. These settings are not read
from or written to `config.json`. Empty or unavailable executables and invalid
timeouts fail startup.

Each executable flag takes a path or a name resolved through `PATH`, not a shell
command. The client starts it without arguments or a shell, inheriting the
client's environment and working directory.

## Exchange

The client writes one UTF-8 JSON object followed by a newline to stdin and closes
stdin. The plugin must write one version 1 JSON object to stdout and exit zero.
Surrounding whitespace is accepted; additional JSON objects or non-whitespace
text are not. Newlines inside strings must be JSON escaped. Diagnostics belong
on stderr.

Each stdout/stderr stream is limited to 128 KiB. The client does not copy stderr
into its logs. A timeout, unsuccessful exit, output overflow or invalid response
is a processing error.

## Request

Outgoing request example:

```json
{"version":1,"plugin":"on_send","content":"Привет!","sender":{"pubkey":"<64 lowercase hex characters>","nick":"alice"}}
```

Incoming request example:

```json
{"version":1,"plugin":"on_receive","content":"Hello!","sender":{"pubkey":"<64 lowercase hex characters>","nick":"bob"},"event_id":"<64 lowercase hex characters>","created_at":1793000000}
```

| Field | Value |
| --- | --- |
| `version` | `1`. |
| `plugin` | `on_send` or `on_receive`; there is no separate direction field. |
| `content` | Original text; incoming text is taken from the validated signed event before display sanitization/truncation. |
| `sender.pubkey` | Lowercase hexadecimal Nostr public key; outgoing requests use the captured destination chat identity. |
| `sender.nick` | Captured outgoing nickname or sanitized incoming display name. |
| `event_id` | Incoming-only signed event ID as lowercase hexadecimal. |
| `created_at` | Incoming-only signed event timestamp in Unix seconds. |

Chat, recipient and private signing keys are not included. Request metadata is
read-only. Unknown request fields may be ignored.

## Response

Return one of:

```json
{"version":1,"action":"unchanged"}
```

```json
{"version":1,"action":"replace","content":"Hello!"}
```

```json
{"version":1,"action":"suppress"}
```

`replace` requires string `content`. For `unchanged` and `suppress`, omit
`content` (JSON `null` is also accepted). Unknown fields are ignored and cannot
change metadata. Unknown actions, unsupported versions, missing replacement
text, invalid UTF-8 and malformed JSON are errors.

## Outgoing processing

`on_send` runs before signing, proof-of-work and publication. The destination,
recipient and identity are already captured; replacement text cannot change
them, including through a leading mention.

The resulting text must be valid UTF-8, nonblank, at most 2000 grapheme clusters
and at most 16 KiB. `suppress` cancels publication and produces a Logs entry.
A processing error prevents publication without fallback to the original text
or automatic retry.

## Incoming processing

Admission, signature/ID verification, deduplication and filter/mute checks run
before `on_receive`. Filter/mute match the sanitized/truncated original text;
mute takes priority. The plugin receives the original signed content only for
messages that pass these checks.

Own messages bypass `on_receive`, including local send feedback and relay echoes
recognized by current or retained session public keys. They display the published
text with the usual display sanitization/truncation; validation and filter/mute
checks still apply.

Replacement changes local display text, not the signed event. The result is
sanitized/truncated but not filtered again. An empty replacement leaves an empty
message row; `suppress` hides the message silently. A processing error logs a
warning and displays the original text.

Blocking by public key is checked before processing and before display. Pending
results are discarded after `/clear` or when their chat is no longer in the
active view.

## Execution limits

Each direction uses a sequential worker: at most one outgoing and one incoming
plugin process run at a time. A full outgoing queue rejects the send. A full
incoming queue drops new messages locally and logs an overload warning.

Shutdown cancels running executables and joins the workers. Plugins run with the
client user's permissions, without a sandbox. The client does not manage
detached descendant processes or route plugin traffic through `--socks`.
