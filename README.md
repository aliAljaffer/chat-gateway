# chat-gateway

A small Go service that turns an OpenCode server into a shared text-only chat
service. It serves a hosted UI, mints per-app API keys, and proxies prompts to
OpenCode over loopback.

The Kubernetes manifests live in
[`aliAljaffer/homelab/kubernetes/apps/chat`](https://github.com/aliAljaffer/homelab/tree/main/kubernetes/apps/chat).
The image is published as `ghcr.io/alialjaffer/chat-gateway`.

## What it does

- Serves an embedded single-page chat UI. Conversations and app profiles live in
  SQLite.
- Verifies Cloudflare Access JWTs for `/api/*`, with a developer bypass for local
  work.
- Mints app keys and stores only their SHA-256 hash.
- Accepts `POST /v1/chat` and `POST /v1/chat/stream` from other applications.
- Creates a short-lived OpenCode session per prompt, sends the app's fixed system
  prompt and the caller's context, streams assistant text deltas, and deletes the
  session.
- Never gives the model a tool. Three controls enforce this: the OpenCode agent
  denies tools, every prompt sets every known tool to false, and callers cannot
  pass a tool definition.

## Layout

```
cmd/gateway            process wiring and shutdown
internal/config        environment parsing
internal/store         SQLite apps, conversations, messages
internal/opencode      OpenCode HTTP client and SSE reader
internal/server        HTTP routes, auth, chat, UI API
internal/web/assets    embedded HTML, CSS, JS
```

## Image

The image is about 202 MiB compressed. Most of it is the Node 22 base and the
OpenCode package. The homelab nodes cache it after the first pull.

The `image` workflow in `.github/workflows/image.yaml` runs the Go checks, builds
for `linux/amd64`, and pushes `:latest` and `:<sha>` to GHCR on every push to
`main`. To build and push by hand:

```bash
bash build.sh latest
```

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `CHAT_ADDR` | `:8080` | Listen address |
| `CHAT_DB` | `/data/gateway.db` | SQLite path |
| `OPENCODE_URL` | `http://127.0.0.1:4096` | OpenCode server |
| `OPENCODE_AGENT` | `assistant` | OpenCode agent name |
| `CHAT_WORKSPACE` | `/workspace` | OpenCode working directory |
| `CHAT_TIMEOUT` | `120` | Whole seconds per prompt |
| `CHAT_RATE_PER_MINUTE` | `30` | Per app key. `0` disables |
| `CHAT_MAX_BODY_BYTES` | `65536` | Request body limit |
| `CF_ACCESS_TEAM_DOMAIN` | empty | Cloudflare Access team domain |
| `CF_ACCESS_AUD` | empty | Cloudflare Access application audience |
| `CHAT_OWNER_EMAIL` | empty | Optional owner email check |
| `CHAT_DEV_UI` | false | Trust `X-Chat-Dev-Email` or a local dev user |
| `CHAT_MODEL` | empty | Display label only |

If `CF_ACCESS_AUD` is empty and `CHAT_DEV_UI` is off, `/api/*` returns 503. The
process still starts so the provider or the Access settings can be added later.

## Local development

You need an OpenCode server. The simplest local setup uses the same text-only
config as the cluster:

```bash
export XDG_CONFIG_HOME=/tmp/chat-gateway-dev/config
export XDG_DATA_HOME=/tmp/chat-gateway-dev/data
mkdir -p "$XDG_CONFIG_HOME" "$XDG_DATA_HOME/opencode"
export OPENCODE_CONFIG=$PWD/dev/opencode.json
cp ~/.local/share/opencode/auth.json "$XDG_DATA_HOME/opencode/auth.json"
opencode serve --hostname 127.0.0.1 --port 4096
```

Then run the gateway with the developer UI bypass:

```bash
CHAT_DEV_UI=1 \
CHAT_DB=/tmp/chat-gateway-dev/gateway.db \
OPENCODE_URL=http://127.0.0.1:4096 \
go run ./cmd/gateway
```

Open `http://127.0.0.1:8080`. The dev user is `dev@localhost`. Use the Apps screen
to create a key, then call the API:

```bash
curl -s -X POST http://127.0.0.1:8080/v1/chat \
  -H "Authorization: Bearer $KEY" \
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

- App keys are 32 random bytes, base64url encoded, shown once. Only the SHA-256
  hash is stored. The key prefix is kept for display.
- The UI is protected at the edge by Cloudflare Access and at the app by JWT
  verification. Mutating `/api/*` requests also require a same-origin request.
- The API host must not sit behind Cloudflare Access. Machines use app keys.
- No request or reply body is logged. Logs carry method, path, status, app id,
  conversation id, and duration.
- `/v1/chat` is stateless. The UI stores conversations in SQLite. The gateway
  does not store API prompts or replies, and the OpenCode session is deleted
  after every prompt. In the deployment, OpenCode state lives in an emptyDir, not
  on the PVC.
- The OpenCode server listens on loopback and is never exposed by a Service.
- Provider keys are written to `auth.json` only at startup and are never logged.

## Tests

```bash
go test ./...
go vet ./...
```

The server tests use a fake OpenCode server and cover prompt construction, tool
denial, validation, auth, rate limiting, SSE deltas, app keys, and the full UI
conversation flow.
