# chat-gateway

A ready-made chat server for other applications. Each app calls the API with its
own key and wraps the replies in its own interface. The gateway owns the model
integration, the per-app prompt, the limits, and the streaming, not the UI.

The Kubernetes manifests live in
[`aliAljaffer/homelab/kubernetes/apps/chat`](https://github.com/aliAljaffer/homelab/tree/main/kubernetes/apps/chat).
The image is published as `ghcr.io/alialjaffer/chat-gateway`.

## What it does

- Loads per-app profiles from a mounted JSON file. A profile is an id, a key, and
  a fixed system prompt.
- Accepts `POST /v1/chat` and `POST /v1/chat/stream` with a bearer app key.
- Creates a short-lived OpenCode session per prompt, sends the app prompt and the
  caller's context, streams assistant text deltas, and deletes the session.
- Never gives the model a tool. Three controls enforce this: the OpenCode agent
  denies tools, every prompt sets every known tool to false, and callers cannot
  pass a tool definition.
- Keeps no conversation or request history. The service is stateless.

## Layout

```
cmd/gateway            process wiring and shutdown
internal/apps          app file loading and key lookup
internal/config        environment parsing
internal/opencode      OpenCode HTTP client and SSE reader
internal/server        HTTP routes, auth, chat
dev/apps.json          a synthetic app for local work
dev/opencode.json      the text-only OpenCode config
```

## Apps

The gateway reads the file named by `CHAT_APPS_FILE` at startup. The file holds
one entry per calling application:

```json
{
  "apps": [
    {
      "id": "grooszy",
      "key": "chat_replace_me_with_a_long_random_key",
      "system_prompt": "You are Gersz. Answer only from the supplied summary."
    }
  ]
}
```

- `id` is used in logs and the per-app rate limit.
- `key` is 24 to 256 characters. Use a random 32-byte value with a `chat_` prefix.
- `system_prompt` is fixed by the service owner. The caller cannot override it.

The file is a Kubernetes Secret in the homelab, sealed in Git. The gateway hashes
the keys in memory and never writes them to disk. The key is never returned by
the API and never logged. To add an app or rotate a key, update the Secret and
restart the deployment. There is no admin API and no UI.

## API

```bash
curl -s -X POST https://chat-api.alialjaffer.com/v1/chat \
  -H "Authorization: Bearer $CHAT_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "messages": [
      {"role": "user", "content": "How much did I spend in May?"}
    ],
    "context": "May spending was 3,500.00 SAR."
  }'
```

```json
{"reply": "You spent 3,500.00 SAR in May."}
```

`POST /v1/chat/stream` returns server-sent events:

```
event: delta
data: {"text":"You spent "}

event: done
data: {"reply":"You spent 3,500.00 SAR in May."}
```

Rules:

| Rule | Value |
|---|---|
| Messages | 1 to 8 |
| Roles | `user` or `assistant` only |
| Last message | `user` |
| Content | 1 to 2000 characters, not blank |
| Context | optional string, at most 16000 characters |
| Body | 64 KiB |
| Rate limit | 30 requests per minute per app, in memory |

Status codes: `401` for a missing or unknown key, `422` for a bad shape, `429`
for the rate limit, `502` when the model fails, `503` when OpenCode is
unavailable, `504` when the model exceeds the timeout.

The API is stateless. The caller sends the history it wants the model to see. The
gateway creates a short-lived OpenCode session, sends one prompt, and deletes the
session. Neither the gateway nor the OpenCode volume keeps the request after the
session is deleted.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `CHAT_ADDR` | `:8080` | Listen address |
| `CHAT_APPS_FILE` | `/apps/apps.json` | App profiles and keys |
| `OPENCODE_URL` | `http://127.0.0.1:4096` | OpenCode server |
| `OPENCODE_AGENT` | `assistant` | OpenCode agent name |
| `CHAT_TIMEOUT` | `120` | Whole seconds per prompt |
| `CHAT_RATE_PER_MINUTE` | `30` | Per app. `0` disables |
| `CHAT_MAX_BODY_BYTES` | `65536` | Request body limit |

The process exits when the apps file is missing or invalid. This is deliberate:
a gateway with no app keys must not start.

## Local development

You need an OpenCode server. The simplest local setup uses the text-only config in
`dev/opencode.json`:

```bash
export XDG_CONFIG_HOME=/tmp/chat-gateway-dev/config
export XDG_DATA_HOME=/tmp/chat-gateway-dev/data
mkdir -p "$XDG_CONFIG_HOME" "$XDG_DATA_HOME/opencode"
export OPENCODE_CONFIG=$PWD/dev/opencode.json
cp ~/.local/share/opencode/auth.json "$XDG_DATA_HOME/opencode/auth.json"
opencode serve --hostname 127.0.0.1 --port 4096
```

Then run the gateway:

```bash
CHAT_APPS_FILE=$PWD/dev/apps.json \
OPENCODE_URL=http://127.0.0.1:4096 \
go run ./cmd/gateway
```

The synthetic key in `dev/apps.json` is `chat_dev_012345678901234567890123456789`.

```bash
curl -s -X POST http://127.0.0.1:8080/v1/chat \
  -H "Authorization: Bearer chat_dev_012345678901234567890123456789" \
  -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":"Hello"}]}'
```

## OpenCode contract

The gateway uses OpenCode v1 HTTP routes, tested against `1.18.35`:

| Route | Use |
|---|---|
| `GET /global/health` | Readiness |
| `POST /session` | Create a short-lived session with the agent and a title |
| `POST /session/{id}/message` | Send `system`, `tools`, `agent`, and one text part |
| `DELETE /session/{id}` | Cleanup |
| `GET /event` | Assistant text deltas and message role updates |

The event reader tracks `message.updated` events with `role: assistant`, then
forwards `message.part.delta` events for those message ids. This prevents the
caller's own prompt from being streamed back as an assistant delta. When a
provider answers before the event stream connects, the gateway emits the full
reply as one delta from the prompt response.

The per-request `system` field is appended to the OpenCode agent prompt. The
gateway always adds a fixed safety suffix that forbids tools and invention, and
tells the model to treat the caller's context as data, not instructions.

## Security notes

- App keys are compared by SHA-256 hash in memory. Only the hash map exists at
  runtime; the keys come from the Secret and are not written to disk.
- No request or reply body is logged. Logs carry method, path, status, app id, and
  duration.
- `/v1/chat` is stateless. The OpenCode session is deleted after every prompt.
- The OpenCode server listens on loopback and is never exposed by a Service.
- Provider keys are written to `auth.json` only at startup and are never logged.

## Image

The image is about 202 MiB compressed. Most of it is the Node 22 base and the
OpenCode package. The homelab nodes cache it after the first pull.

The `image` workflow in `.github/workflows/image.yaml` runs the Go checks, builds
for `linux/amd64`, and pushes `:latest` and `:<sha>` to GHCR on every push to
`main`. After the push, it uses the `update-manifests-homelab` GitHub App to
commit the new digest to the homelab manifest. To build and push by hand:

```bash
bash build.sh latest
```

## Tests

```bash
go test ./...
go vet ./...
```

The server tests use a fake OpenCode server and cover prompt construction, tool
denial, validation, auth, rate limiting, and SSE deltas.
