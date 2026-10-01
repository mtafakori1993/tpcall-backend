# tpcall-backend

Internal company VoIP-style calling backend (WebRTC signaling) for the Android
`tpcall` application.

A single Go service that provides:

* REST API (register, login, user lookup, ICE config, health)
* Authenticated WebSocket signaling for WebRTC calls
* PostgreSQL persistence (users only; active calls stay in memory)
* JWT access tokens + Argon2id password hashing
* Presence tracking and in-memory call state machine

The backend **never** relays WebRTC audio/video. Android devices establish peer
connections directly over the LAN; the backend only exchanges
`offer` / `answer` / `ice_candidate` and call control messages.

Target LAN address for clients:

```text
REST:    http://192.168.1.200:9090
WebSocket: ws://192.168.1.200:9090/api/v1/ws
```

---

## 1. Quick start (Docker)

```bash
# 1. Configure (at least a real JWT secret)
cp .env.example .env
# edit .env and set JWT_SECRET to a long random value, e.g.
#   JWT_SECRET=$(openssl rand -base64 48)

# 2. Build and start
docker compose up --build

# 3. Health check
curl http://localhost:9090/health
curl http://192.168.1.200:9090/health   # from the machine that owns that IP
```

PostgreSQL healthcheck + backend retry logic make the backend start cleanly
even before the database is ready.

### Test registration / login

```bash
# Register (101 and 203)
curl -X POST http://192.168.1.200:9090/api/v1/auth/register \
  -H "Content-Type: application/json" \
  -d '{"name":"Mohammad Tafakori","extension":"101","password":"12345678"}'

curl -X POST http://192.168.1.200:9090/api/v1/auth/register \
  -H "Content-Type: application/json" \
  -d '{"name":"Hesabdari","extension":"203","password":"12345678"}'

# Login
curl -X POST http://192.168.1.200:9090/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"extension":"101","password":"12345678"}'
```

## 2. Run without Docker (local development)

```bash
# needs PostgreSQL reachable at DATABASE_URL
export DATABASE_URL='postgres://tpcall:tpcall_password@localhost:5432/tpcall?sslmode=disable'
export JWT_SECRET='some-long-random-secret'
go run ./cmd/server
```

Migrations run automatically at startup (see §10).

## 3. Configuration

All configuration comes from environment variables (see `.env.example`):

| Variable | Default | Description |
| --- | --- | --- |
| `HTTP_HOST` | `0.0.0.0` | Bind address (must stay `0.0.0.0` for LAN phones) |
| `HTTP_PORT` | `9090` | Listen port |
| `PUBLIC_BASE_URL` | `http://192.168.1.200:9090` | Advertised URL (logging/docs only) |
| `DATABASE_URL` | *(required)* | PostgreSQL connection string |
| `JWT_SECRET` | *(required)* | JWT signing secret; startup fails if missing |
| `JWT_EXPIRATION` | `24h` | Access token lifetime |
| `RING_TIMEOUT` | `30s` | How long a call rings before it is cancelled |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `CORS_ORIGINS` | `*` | Comma-separated allowed origins |
| `ICE_SERVERS` | *(empty)* | Comma-separated ICE URLs, e.g. `stun:stun.l.google.com:19302` |
| `MIGRATIONS_DIR` | `./migrations` | SQL migrations directory |

The server must bind to `0.0.0.0:9090` so Android phones on the LAN can reach
it at `192.168.1.200:9090`.

## 4. REST API

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/health` | no | Liveness + DB connectivity |
| `POST` | `/api/v1/auth/register` | no | Register employee, returns tokens |
| `POST` | `/api/v1/auth/login` | no | Login with extension + password |
| `GET` | `/api/v1/me` | yes | Current user + online status |
| `GET` | `/api/v1/users` | yes | All users sorted by extension |
| `GET` | `/api/v1/users/{extension}` | yes | Look up one user |
| `GET` | `/api/v1/webrtc/config` | yes | ICE servers for the app |
| `GET` | `/api/v1/ws` | yes* | WebSocket signaling endpoint |

`*` The WebSocket endpoint authenticates with `Authorization: Bearer <JWT>`
(preferred) or `?token=<JWT>`.

### Validation rules

* `name`: required, trimmed, 2–100 characters.
* `extension`: exactly three digits, `100`–`999`. Rejected: `001`, `020`, `099`, `abc`, `10`, `1000`. Stored as `VARCHAR(3)` with a DB CHECK constraint.
* `password`: 6–128 characters, hashed with Argon2id.

### Register

```http
POST /api/v1/auth/register
```

```json
{ "name": "محمد تفکری", "extension": "101", "password": "12345678" }
```

```json
201
{
  "access_token": "...",
  "token_type": "Bearer",
  "user": { "id": "uuid", "name": "محمد تفکری", "extension": "101" }
}
```

Duplicate extension:

```json
409
{ "error": "extension_already_exists", "message": "extension already exists" }
```

### Login

```http
POST /api/v1/auth/login
```

```json
{ "extension": "101", "password": "12345678" }
```

Invalid extension or password always returns the same response so the two are
indistinguishable:

```json
401
{ "error": "invalid_credentials", "message": "invalid extension or password" }
```

### JWT claims

```json
{ "sub": "<user UUID>", "extension": "101", "name": "محمد تفکری", "exp": 1720000000 }
```

## 5. WebSocket protocol

All messages are JSON envelopes:

```json
{ "type": "call", "request_id": "uuid", "payload": {} }
```

`request_id` is optional and echoed back on acknowledgments/errors.

### Message types

| Type | Direction | Meaning |
| --- | --- | --- |
| `call` | client → server | Start a call, payload `{"to":"203"}` |
| `incoming_call` | server → callee | `{"call_id":"...","from":{"extension","name"}}` |
| `call` | server → caller | Ringing ack: `{"call_id","status":"ringing","to":{...}}` |
| `accept` / `accepted` | client → server / server → caller | Callee accepts |
| `reject` / `rejected` | client → server / server → caller | Callee rejects |
| `busy` | server → caller | `{"extension":"203"}` destination is in another call |
| `unavailable` | server → caller | `{"extension":"203","reason":"offline"}` |
| `offer` / `answer` | participant → server → peer | SDP relay |
| `ice_candidate` | participant → server → peer | ICE candidate relay |
| `hangup` | client → server | End the call |
| `call_ended` | server → peer | `{"call_id","reason"}` reason: `remote_hangup`, `peer_disconnected`, `no_answer`, `ring_timeout` |
| `error` | server → client | `{"code","message"}` |
| `ping` / `pong` | both | Application heartbeat (protocol ping/pong also used) |

`call_id` is always generated server-side.

### Call flow

```
101 connects websocket
203 connects websocket

101 -> call(to=203)

server -> 203 incoming_call
server -> 101 call{status=ringing}

203 -> accept(call_id)
server -> 101 accepted

101 -> offer
server -> 203 offer

203 -> answer
server -> 101 answer

101/203 -> ice_candidate
server -> peer

audio flows directly over WebRTC (backend never touches it)

101 -> hangup
server -> 203 call_ended{reason=remote_hangup}
```

### Reject

```
101 -> call 203
203 -> reject
101 <- rejected
```

### Busy

```
203 already in a call
101 -> call 203
101 <- busy{extension=203}
```

A caller that is already in a call also cannot start a second call; it receives
`error{already_in_call}`.

### Offline

```
203 disconnected
101 -> call 203
101 <- unavailable{extension=203, reason=offline}
```

### Errors

```json
{ "type": "error", "request_id": "...", "payload": { "code": "invalid_message", "message": "..." } }
```

Common codes: `invalid_message`, `invalid_payload`, `user_not_found`,
`cannot_call_self`, `already_in_call`, `call_not_found`,
`not_call_participant`, `invalid_sender`, `invalid_call_state`,
`session_replaced`.

### Behavior notes

* Only **one** active session per extension. A new WebSocket for the same
  extension replaces the old one: the old connection receives
  `error{session_replaced}` and is closed, and any call it was in is ended.
* Presence is derived from live WebSocket connections; `online` in REST
  responses reflects that.
* Dead connections are removed via protocol ping/pong (ping every 25 s,
  considered dead after ~60 s) and a bounded per-client send queue (64
  messages). A client that stops consuming is disconnected.
* A malformed application message returns `error{invalid_message}` and does
  not disconnect the socket.

## 6. WebRTC scope

The backend does **not** implement audio encoding, mic capture, PCM streaming,
an SFU/MCU, or SIP. It only relays `offer`, `answer`, `ice_candidate`, and call
control. `GET /api/v1/webrtc/config` returns ICE configuration so clients don't
hardcode it:

```json
{ "iceServers": [] }
```

Set `ICE_SERVERS=stun:stun.l.google.com:19302` to advertise a STUN server.
For a company LAN, host candidates are usually sufficient without any STUN.

## 7. Android note (cleartext)

The Android app will initially talk to `http://192.168.1.200:9090` and
`ws://192.168.1.200:9090/api/v1/ws` (plain HTTP/WS over the LAN). Android
blocks cleartext traffic by default, so the app needs either:

```xml
<application android:usesCleartextTraffic="true" ...>
```

or a network security config allowing cleartext for `192.168.1.200`.

## 8. Tests

```bash
gofmt -w .
go vet ./...
go test ./...
go test -race ./...
```

Tests cover authentication (register/login/validation/protected endpoints) and
signaling (call, accept, reject, busy, offline, offer/answer/ICE routing,
authorization, hangup, disconnect cleanup, ring timeout, session replacement)
without requiring a database (in-memory user store).

## 9. Logging

Structured JSON logs to stdout with event names such as `server_started`,
`user_registered`, `login_success`, `login_failed`, `websocket_connected`,
`websocket_disconnected`, `call_started`, `call_accepted`, `call_rejected`,
`call_busy`, `call_unavailable`, `call_ended`. Passwords, tokens, password
hashes, and full SDP/ICE payloads are never logged.

## 10. Database

PostgreSQL schema is defined in `migrations/0001_init.sql`:

```sql
CREATE TABLE users (
    id            UUID PRIMARY KEY,
    name          VARCHAR(100) NOT NULL,
    extension     VARCHAR(3)   NOT NULL UNIQUE,
    password_hash TEXT         NOT NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    last_login_at TIMESTAMPTZ,
    CONSTRAINT chk_users_extension CHECK (extension ~ '^[1-9][0-9]{2}$')
);
```

Active WebRTC calls are intentionally **not** persisted (memory only).

### Migrations

Migrations are plain SQL files in `migrations/`, applied automatically at
startup in filename order inside a transaction and tracked in the
`schema_migrations` table. To add a migration, drop a new
`0002_*.sql` file in that directory and restart.

## 11. Project layout

```text
cmd/server/            entrypoint
internal/auth/         JWT + Argon2id + register/login service
internal/calls/        in-memory call state machine (atomic transitions)
internal/config/       environment configuration
internal/database/     pgx pool + migration runner
internal/httpapi/      REST handlers + router
internal/httpx/        shared JSON helpers
internal/logging/      structured logger
internal/middleware/   request ID, logging, auth, recover
internal/signaling/    WebSocket hub, presence, protocol
internal/users/        user store (PostgreSQL + in-memory for tests)
migrations/            SQL migrations
```

## 12. Known limitations (MVP scope)

* One call per extension at a time; no call history, recordings, or duration
  persistence.
* No password reset / email / SMS / admin portal.
* No TURN server bundled (add a private TURN later via `ICE_SERVERS` if the LAN
  topology needs it).
* Presence is in-memory, so a backend restart drops all sessions (clients
  simply reconnect).
* Ring timeout is fixed per process via `RING_TIMEOUT`.
* WebSocket sender identity is always taken from the authenticated JWT; fields
  like `from` are never trusted.
