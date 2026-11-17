# Game Plan: 2D Top-Down Shooter (Go server + C++ client over UDP)

## Goal
A simple multiplayer 2D top-down shooter. The Go server decides everything; the C++ client sends inputs and draws the game state.

## Stack
| Part | Tech |
|---|---|
| Server | Go: `net` (UDP game loop), `github.com/gin-gonic/gin` (HTTPS API), `golang.org/x/crypto/chacha20poly1305` |
| Client | C++17, raylib (render/input), plain UDP sockets (POSIX/Winsock wrapper), `cpp-httplib` with OpenSSL (HTTPS), libsodium (AEAD) |
| Build | Go modules; CMake + FetchContent for the client (raylib, cpp-httplib); libsodium + OpenSSL from the system package manager |

## Layout
```
repo/
├── server/                    DDD layers; dependencies point inward
│   ├── main.go, run.go       composition root: flags, wiring, game loop goroutine
│   └── internal/
│       ├── domain/match/     DOMAIN: Match aggregate, Player/Bullet entities, Wall value
│       │                     object, rules + movement, domain events (Joined/Left/Killed/Respawned).
│       │                     No imports from the rest of the server.
│       ├── app/              APPLICATION: Service drives Match from messages, maps domain
│       │                     events to reliable protocol events, sends snapshots.
│       │                     Transport port (interface) for outbound packets.
│       ├── protocol/         wire format + reliable events (Outbox, AckTracker)
│       ├── api/              INTERFACE: Gin HTTPS routes (/join, /status, /healthz)
│       └── infra/
│           ├── aead/         ChaCha20-Poly1305 envelope, replay window
│           ├── session/      session table (token, key, addr, counters)
│           ├── udp/          socket adapter: implements app.Transport, read loop
│           └── tlscert/      self-signed dev cert
├── client/
│   ├── CMakeLists.txt
│   ├── main.cpp      game loop, rendering
│   ├── api.cpp/.h    HTTP /join via cpp-httplib
│   ├── net.cpp/.h    UDP socket, reliable-event acks/dedupe
│   ├── crypto.cpp/.h libsodium seal/open, replay window (mirrors crypto.go)
│   ├── proto.h       binary encode/decode (mirrors proto.go)
│   ├── game_shared.h movement + constants (mirrors game.go, for prediction)
│   └── tests/core_test.cpp
└── .work/plan.md
```

## HTTP API (Gin, HTTPS only, :8443)
- TLS via `r.RunTLS` (self-signed cert for local dev; the client pins it or uses a dev CA flag).
- `POST /join {name}` → `{token, key, playerId, udpAddr}`. `token` is a random 64-bit session id; `key` is a random 32-byte session key (base64), both from `crypto/rand` and stored in the session table. The key only ever travels inside TLS.
- `GET /status` → player count, tick rate.
- `GET /healthz` → 200.
- Gin logger + recovery middleware. Room for later routes (leaderboard, server list).

## Networking (UDP, :7777)
- The client calls `/join`, then sends HELLO with its token every 200 ms until WELCOME arrives. The server binds the token to the source addr:port and rebinds if the address changes (NAT rebinding).
- Every packet (both directions) is encrypted; see Security. The server drops packets with an unknown token or that fail to decrypt.
- INPUT and SNAPSHOT are unreliable (fire and forget).
- Reliable events (WELCOME, KILL, RESPAWN, chat):
  - each event carries a `u16 eventSeq`
  - the client acks with `u16 ack | u32 ackBits` inside every INPUT
  - the server resends unacked events every 100 ms; the client dedupes by eventSeq
- Keepalive: INPUT already flows at ~60/s. Drop the session after 5 s of silence.
- Keep every packet under 1200 bytes.

## Security (from milestone 1, not later)
- Every UDP packet on the wire:
  ```
  u64 token | u64 counter | ciphertext(payload) | 16-byte Poly1305 tag
  ```
  - ChaCha20-Poly1305 (IETF) with the session key. 12-byte nonce = `u32 direction (0 = c→s, 1 = s→c) | u64 counter`.
  - `token` and `counter` are sent in clear but are the AEAD associated data, so tampering fails the tag.
  - Each side keeps a per-direction counter starting at 1 and never reuses it. Close the session before the counter could wrap.
- Replay protection: receiver tracks the highest counter + a 64-bit bitmask window; drop duplicates and anything older than the window.
- Packets that fail to open are dropped silently (no reply, no logging per packet, to avoid amplification and log spam).
- Overhead is 32 bytes per packet; keep the plaintext payload under 1150 bytes.
- Out of scope: cheating (the client owns its key; server-side validation handles that) and volumetric DDoS.

## Protocol (little-endian binary)
```
Plaintext payloads (each is wrapped in the encrypted envelope from Security)

Client → Server
  HELLO    u8 type=0
  PING     u8 type=3 | u32 clientTimeUs (opaque, wraps) (server echoes as PONG type=4)
  INPUT    u8 type=1 | u32 seq | i8 moveX | i8 moveY | f32 aimAngle | u8 buttons(bit0=shoot)
           | u16 eventAck | u32 eventAckBits

Server → Client (unreliable, 30/s)
  SNAPSHOT u8 type=2 | u32 tick | u32 lastAckedSeq | u8 nPlayers
           { u8 id | f32 x | f32 y | f32 angle | u8 hp | u8 alive } * n
           | u8 nBullets { u16 id | f32 x | f32 y } * n

Server → Client (reliable via eventSeq)
  WELCOME  u8 type=10 | u16 eventSeq | u8 yourId | u16 mapW | u16 mapH | u8 nWalls { u16 x,y,w,h } * n
  KILL     u8 type=11 | u16 eventSeq | u8 killer | u8 victim
  RESPAWN  u8 type=12 | u16 eventSeq | u8 id | f32 x | f32 y
  PLAYER_INFO u8 type=13 | u16 eventSeq | u8 id | u8 nameLen | name
  PLAYER_LEFT u8 type=14 | u16 eventSeq | u8 id
```

## Server (Go)
- Concurrency: one goroutine reads UDP into a channel; the tick goroutine owns the world, so game state needs no locks. Gin runs in its own goroutine and shares a mutex-guarded session table with udp.go.
- Tick at 30 Hz: apply the latest input per player, move players and bullets, check collisions, broadcast a snapshot, resend unacked events.
- Bullets are projectiles with speed, lifetime and circle-vs-circle hits.
- Fire rate is capped on the server (e.g. 5 shots/s).
- HP 100, damage 25, respawn after 3 s at a random spawn.
- Clamp movement to the map; validate input ranges (anti-cheat basics).
- Remove a player after 5 s with no packets.

## Client (C++)
- raylib window 1280x720; WASD to move, mouse to aim, LMB to shoot.
- Non-blocking UDP socket, polled every frame. Send INPUT every frame.
- Keep a snapshot buffer and draw remote players about 100 ms in the past, interpolated.
- Local player: client-side prediction plus reconciliation using `lastAckedSeq`.
- HUD: HP, ping, kill feed.

## Milestones
1. **Secure join + handshake**: HTTPS `/join` returns token + key; encrypted UDP HELLO → WELCOME; replay window; ping/pong shows RTT.
2. **One player moving**: inputs to the server, snapshots back, draw a circle.
3. **Multiplayer**: several clients, all players drawn, join/leave handled.
4. **Shooting**: bullets, hits, HP, death/respawn, kill feed (reliable events).
5. **Smoothness**: interpolation for remote players, prediction for the local player.
6. **Polish**: map walls, scoreboard, simple sounds.

## Testing
- Run the server plus 2–3 clients locally.
- Simulate bad networks: `sudo tc qdisc add dev lo root netem delay 80ms loss 5%` (remove with `tc qdisc del dev lo root`). Reliable events must still arrive.
- Go unit tests: AEAD round trip, tampered/replayed/wrong-key packets rejected, Go↔C++ cross-check with a fixed test vector, protocol encode/decode, hit detection, the reliable-event layer (resend, dedupe, ack bits), and `/join` via `httptest`.

## Status (2026-11-17)
All milestones implemented. Run:
```
cd server && go run .                      # creates certs/server.crt on first run
cd client && cmake -S . -B build && cmake --build build -j
./build/shooter --ca ../server/certs/server.crt --name alice
```
Tests: `cd server && go test -race ./...`, `cd client/build && ctest`.
