# translate-plugin

A standalone, standard-library-only Go example of an outgoing strchat-tui
translation plugin. It reads one JSON request line from standard input and
writes exactly one JSON response line to standard output.

See [PROTOCOL.md](PROTOCOL.md) for the message protocol and recommendations for
writing your own plugins in any language.

The plugin translates outgoing `on_send` messages with GigaChat's
OpenAI-compatible Chat Completions API. Requests for other events, or for
incoming messages, return `unchanged` without any network traffic.

## Build

From a full repository checkout:

```sh
CGO_ENABLED=0 go build -trimpath \
  -o "$HOME/.local/bin/translate-plugin" \
  ./examples/translate-plugin
```

The plugin takes no command-line arguments. It can also be run directly:

```sh
go run ./examples/translate-plugin
```

## Request and response

The request is one JSON line. Unknown fields are ignored, so newer request
fields do not require a plugin change.

```json
{"version":1,"plugin":"on_send","direction":"outgoing","chat":"lobby","content":"Привет, @bob#abcd!","sender":{"pubkey":"abc123","nick":"alice"},"future_field":true}
```

A successful outgoing translation returns:

```json
{"version":1,"action":"replace","content":"Hi, @bob#abcd!"}
```

Any request that is not `plugin:"on_send"` with `direction:"outgoing"` (or that
has blank content) returns:

```json
{"version":1,"action":"unchanged"}
```

The plugin does not implement a suppression action. Response strings are JSON
encoded, so a translated message containing newlines still produces exactly one
output line.

## Configuration

The plugin reads its own process environment. Export these variables before
starting `strchat-tui`, or set them in the service unit that launches it.

| Variable | Default | Purpose |
| --- | --- | --- |
| `TRANSLATE_PLUGIN_BASE_URL` | `https://api.giga.chat/v1` | OpenAI-compatible API base. The plugin appends `/chat/completions`. |
| `TRANSLATE_PLUGIN_AUTH_URL` | `https://ngw.devices.sberbank.ru:9443/api/v2/oauth` | GigaChat OAuth token endpoint. |
| `TRANSLATE_PLUGIN_MODEL` | `GigaChat-2` | Chat Completions model name. |
| `TRANSLATE_PLUGIN_API_KEY` | unset | GigaChat authorization key for OAuth, required unless an access token is supplied. |
| `TRANSLATE_PLUGIN_ACCESS_TOKEN` | unset | Optional access token. Takes precedence over the API key and skips OAuth. |
| `TRANSLATE_PLUGIN_SCOPE` | `GIGACHAT_API_PERS` | OAuth scope. Use the scope matching the authorization key (`GIGACHAT_API_B2B` or `GIGACHAT_API_CORP` where applicable). |
| `TRANSLATE_PLUGIN_TARGET_LANGUAGE` | `English` | Language written into the translation instruction. |

### Authorization key

Set `TRANSLATE_PLUGIN_API_KEY` to the GigaChat authorization key from GigaChat
Studio: the already-Base64-encoded `client_id:client_secret` value, without a
`Basic` prefix or an `Authorization` header. The plugin does not read credential
files. A missing/blank key fails before any HTTP request unless
`TRANSLATE_PLUGIN_ACCESS_TOKEN` is set.

In Bash, enter the key without echoing it or placing its value in shell history:

```sh
read -r -s -p 'GigaChat authorization key: ' TRANSLATE_PLUGIN_API_KEY
printf '\n'
export TRANSLATE_PLUGIN_API_KEY
```

Start the client from the same shell so the plugin inherits the variable. An
existing `TRANSLATE_PLUGIN_ACCESS_TOKEN` takes precedence; unset it to use OAuth.
After the client exits, use `unset TRANSLATE_PLUGIN_API_KEY` if it is no longer
needed. Neither credential is written to stdout/stderr or passed in executable
arguments. Environment variables are still secrets: avoid shell tracing, dumping
the environment, or exposing it in diagnostics; they are not an OS security
boundary against other processes with access to your environment.

### Enable for a launch

Pass the built executable to `--plugin-send`. The client does not store plugin
settings in `config.json`. Its default plugin timeout is 10 seconds (maximum
2 minutes), while this example bounds a run to 30 seconds. Allow slightly more
time for the example to report its own error instead of being killed:

```sh
./strchat-tui \
  --plugin-send="$HOME/.local/bin/translate-plugin" \
  --plugin-timeout=35s
```

The executable is invoked once per outgoing message and receives the plugin
request on standard input. It inherits the environment above from the process
that starts `strchat-tui`. The plugin takes no command-line arguments.

## TLS certificate verification

TLS certificate verification is always enabled. There is no insecure-skip
option, and this example never sets `InsecureSkipVerify`.

GigaChat requires the Russian Trusted Root CA; see its
[official certificate instructions](https://developers.sber.ru/docs/ru/gigachat/certificates).
Go uses the host's system trust store. If that CA is missing, install it there
(recommended on macOS and Windows). On Unix-like hosts, Go can instead use a PEM
bundle for the plugin process:

```sh
export SSL_CERT_FILE="$HOME/.config/gigachat/trusted-ca.pem"
```

On Unix, `SSL_CERT_DIR` can point at a directory of hashed CA certificates. If a
TLS-intercepting proxy is in use, add the proxy CA to the same trust bundle.
Use `https://` endpoints for real services; plain `http://` is only useful for
local mock servers.

## Failure behavior

- Unsupported request versions, malformed requests, missing credentials, and
  API failures exit with status 1.
- On failure, standard output stays empty. A sanitized diagnostic is written to
  standard error with the `translate-plugin:` prefix.
- API response bodies, authorization keys, and access tokens are never logged.
- A run is bounded to 30 seconds (the HTTP client also has a 30-second timeout).
  Request lines and decoded HTTP response input are limited to 1 MiB.

## Tests

The tests are hermetic and use `httptest` servers only; they never contact the
real API or read a real credential file.

```sh
go test ./examples/translate-plugin
go test -race ./examples/translate-plugin
go vet ./examples/translate-plugin
```
