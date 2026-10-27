# translate-plugin

Translates strchat-tui messages with GigaChat: `on_send` into French and
`on_receive` into Russian by default. The implementation uses only the Go
standard library and takes no command-line arguments.

## Build

Run from the repository root:

```sh
CGO_ENABLED=0 go build -trimpath -o translate-plugin ./examples/translate-plugin
```

## Launch

Set the OAuth authorization key in Bash:

```sh
read -r -s -p 'GigaChat authorization key: ' TRANSLATE_PLUGIN_API_KEY
printf '\n'
export TRANSLATE_PLUGIN_API_KEY

./strchat-tui \
  --plugin-send="$PWD/translate-plugin" \
  --plugin-receive="$PWD/translate-plugin" \
  --plugin-timeout=35s
```

The key is the Base64 authorization value, without the `Basic` prefix. The plugin
uses the value directly; it does not encode it or read credential files.
`TRANSLATE_PLUGIN_ACCESS_TOKEN`, when nonblank, takes precedence and skips OAuth.

The client starts a new plugin process for each outgoing or admitted incoming
message. The plugin inherits the client's environment. Its HTTP operations share a 30-second context;
the HTTP client also has a 30-second timeout. The launch above sets the client's
process timeout to 35 seconds instead of the default 10 seconds.

The client does not run `on_receive` for its own messages, including local send
feedback and recognized relay echoes. Your outgoing French text therefore stays
French on your screen; other senders' admitted messages are translated into
Russian. Validation and filter/mute checks still apply to both.

## Input and output

The process reads the first JSON line from stdin, writes one JSON line to stdout
and exits. The input limit is 1 MiB, including the newline. EOF without a final
newline is accepted; subsequent lines are not processed.

The plugin reads `version`, `plugin` and `content`. Unknown fields are ignored.
`version` must be `1`. `plugin:"on_send"` and `plugin:"on_receive"` with nonblank
content trigger an API request; `sender` and other metadata do not affect
translation.

```json
{"version":1,"plugin":"on_send","content":"Привет!"}
```

Successful outgoing translation:

```json
{"version":1,"action":"replace","content":"Bonjour !"}
```

For `plugin:"on_receive"`, `content:"Bonjour !"` is translated into Russian:

```json
{"version":1,"action":"replace","content":"Привет!"}
```

Other handler names, or content that is empty after trimming whitespace, return
the following without HTTP requests or credential checks:

```json
{"version":1,"action":"unchanged"}
```

The plugin never returns `suppress`. Newlines within response content are JSON
escaped.

## Configuration

Values are read from environment variables and trimmed. Blank values use the
defaults below; credentials have no default.

| Variable | Default | Purpose |
| --- | --- | --- |
| `TRANSLATE_PLUGIN_BASE_URL` | `https://api.giga.chat/v1` | API base; trailing slashes are removed before appending `/chat/completions`. |
| `TRANSLATE_PLUGIN_AUTH_URL` | `https://ngw.devices.sberbank.ru:9443/api/v2/oauth` | OAuth endpoint. |
| `TRANSLATE_PLUGIN_MODEL` | `GigaChat-2` | Model sent to the API. |
| `TRANSLATE_PLUGIN_API_KEY` | unset | Authorization key used as `Authorization: Basic <key>`. |
| `TRANSLATE_PLUGIN_ACCESS_TOKEN` | unset | Token used as `Authorization: Bearer <token>`; skips OAuth. |
| `TRANSLATE_PLUGIN_SCOPE` | `GIGACHAT_API_PERS` | OAuth scope. |
| `TRANSLATE_PLUGIN_TARGET_LANGUAGE` | `French` | Outgoing target language in the system instruction. |
| `TRANSLATE_PLUGIN_RECEIVE_LANGUAGE` | `Russian` | Incoming target language in the system instruction. |

## API requests

Without an access token, each invocation first sends an OAuth `POST` with
`Content-Type: application/x-www-form-urlencoded`, a generated UUID v4 in `RqUID`
and a `scope` form field. It reads `access_token` from the JSON response. Tokens
are not cached between processes.

Translation uses `POST <base>/chat/completions` with a JSON body containing
`model` and two `messages`: a system instruction and the original content as a
user message. The instruction requests translation only, preservation of
mentions, URLs, code and line breaks, and treatment of the message as data.
These are model instructions, not output validation.

The plugin takes `choices[0].message.content`, trims surrounding whitespace and
rejects an empty result. Each HTTP response decoder reads at most 1 MiB. HTTP
statuses outside 200–299 are errors. There are no application-level retries.

HTTPS uses Go's default certificate verification. The client's `--socks` option
does not configure the plugin's HTTP client. Outgoing text is sent to the API
before Nostr publication. Incoming text is sent only after validation and
filter/mute checks; translation changes local presentation, not the signed event.

## Errors

Malformed JSON, unsupported versions, oversized input, missing credentials,
HTTP failures and missing token/translation fields cause exit status `1` and a
`translate-plugin:` diagnostic on stderr, without a protocol response on stdout.
API response bodies and authorization header values are not included in these
diagnostics. An absent key or a key containing internal CR/LF fails before HTTP
unless an access token is supplied.

An outgoing plugin error prevents publication; the client does not send the
original text as a fallback.
An incoming plugin error logs a warning and displays the admitted original text.

## Tests

```sh
go test ./examples/translate-plugin
go vet ./examples/translate-plugin
```
