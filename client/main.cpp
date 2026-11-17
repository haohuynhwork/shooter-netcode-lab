// 2D top-down shooter client: raylib rendering, encrypted UDP to the Go server.
//
// Usage: shooter [--server https://localhost:8443] [--ca certs/server.crt] [--name NAME]
#include <raylib.h>

#include <algorithm>
#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <deque>
#include <map>
#include <string>
#include <vector>

#include "api.h"
#include "game_shared.h"
#include "net.h"
#include "proto.h"

namespace {

constexpr int kScreenW = 1280, kScreenH = 720;
constexpr double kInputDt = 1.0 / game::kInputRate;
constexpr double kInterpDelay = 0.1;  // draw remote players 100 ms in the past
constexpr double kHelloInterval = 0.2;
constexpr double kConnectTimeout = 10.0;
constexpr double kServerSilence = 5.0;

struct Args {
    std::string server = "https://localhost:8443";
    std::string ca = "certs/server.crt";
    std::string name = "player";
};

Args parseArgs(int argc, char** argv) {
    Args a;
    for (int i = 1; i < argc; i++) {
        std::string k = argv[i];
        auto next = [&](std::string& dst) {
            if (i + 1 < argc) dst = argv[++i];
        };
        if (k == "--server") next(a.server);
        else if (k == "--ca") next(a.ca);
        else if (k == "--name") next(a.name);
        else if (k == "-h" || k == "--help") {
            std::puts("shooter [--server https://host:8443] [--ca server.crt] [--name NAME]");
            std::exit(0);
        }
    }
    return a;
}

// Wrapping microsecond timestamp. Go through uint64 first: converting an
// out-of-range double straight to uint32 is undefined behaviour.
uint32_t micros32(double seconds) { return uint32_t(uint64_t(seconds * 1e6)); }

// ---- sounds (generated, no asset files) ----

Sound makeSound(float seconds, float freqStart, float freqEnd, float noise, float volume) {
    const int rate = 22050;
    const int frames = int(seconds * rate);
    auto* data = static_cast<short*>(std::malloc(sizeof(short) * frames));  // freed by UnloadWave
    float phase = 0;
    unsigned seed = 12345;
    for (int i = 0; i < frames; i++) {
        float t = float(i) / frames;
        float f = freqStart + (freqEnd - freqStart) * t;
        phase += f / rate;
        float sq = std::fmod(phase, 1.0f) < 0.5f ? 1.0f : -1.0f;
        seed = seed * 1103515245u + 12345u;
        float nz = float((seed >> 16) & 0x7fff) / 16384.0f - 1.0f;
        float env = (1.0f - t) * (1.0f - t);
        data[i] = short((sq * (1 - noise) + nz * noise) * env * volume * 32767);
    }
    Wave w{};
    w.frameCount = unsigned(frames);
    w.sampleRate = rate;
    w.sampleSize = 16;
    w.channels = 1;
    w.data = data;
    Sound s = LoadSoundFromWave(w);
    UnloadWave(w);
    return s;
}

// ---- interpolation ----

struct TimedSnapshot {
    proto::Snapshot snap;
};

float lerpAngle(float a, float b, float t) {
    float d = std::fmod(b - a + 3 * PI, 2 * PI) - PI;
    return a + d * t;
}

Color playerColor(uint8_t id) {
    static const Color palette[] = {
        {230, 41, 55, 255},  {0, 121, 241, 255}, {0, 158, 47, 255},   {255, 161, 0, 255},
        {200, 122, 255, 255}, {0, 190, 190, 255}, {255, 109, 194, 255}, {127, 106, 79, 255},
    };
    return palette[id % 8];
}

struct KillFeedEntry {
    std::string text;
    double time;
};

struct Client {
    Args args;
    net::Connection* conn = nullptr;

    bool welcomed = false;
    int myId = -1;
    game::Map map;
    std::map<uint8_t, std::string> names;

    // snapshots, oldest first
    std::deque<proto::Snapshot> snaps;
    double clockOffset = 0;  // localTime - serverTime, smoothed
    bool haveClock = false;
    double lastPacketTime = 0;

    // local prediction
    float predX = 0, predY = 0;
    bool meAlive = false;
    uint32_t inputSeq = 0;
    std::deque<proto::Input> pending;
    double lastShotTime = -1;

    double pingMs = -1;
    std::deque<KillFeedEntry> feed;
    Sound sndShoot{}, sndDeath{}, sndHit{};
    bool audio = false;

    std::string name(uint8_t id) const {
        auto it = names.find(id);
        return it == names.end() ? "#" + std::to_string(id) : it->second;
    }

    const proto::PlayerState* latestMe() const {
        if (snaps.empty()) return nullptr;
        for (const auto& p : snaps.back().players)
            if (p.id == myId) return &p;
        return nullptr;
    }

    void onSnapshot(const proto::Snapshot& s, double now) {
        if (!snaps.empty() && int32_t(s.tick - snaps.back().tick) <= 0) return;  // old/dup
        snaps.push_back(s);
        while (snaps.size() > 32) snaps.pop_front();

        double serverTime = double(s.tick) / game::kTickRate;
        double off = now - serverTime;
        if (!haveClock) { clockOffset = off; haveClock = true; }
        else if (off < clockOffset) clockOffset = off;  // a faster packet: adopt
        else clockOffset += (off - clockOffset) * 0.05;  // drift slowly toward later

        // Reconcile: start from the server's authoritative position, replay
        // inputs it hasn't applied yet.
        for (const auto& p : s.players) {
            if (p.id != myId) continue;
            bool wasAlive = meAlive;
            meAlive = p.alive;
            while (!pending.empty() && int32_t(pending.front().seq - s.lastAckedSeq) <= 0) pending.pop_front();
            predX = p.x;
            predY = p.y;
            if (meAlive) {
                for (const auto& in : pending) game::movePlayer(predX, predY, in.moveX, in.moveY, map);
            } else {
                pending.clear();
            }
            if (wasAlive && !meAlive && audio) PlaySound(sndDeath);
        }
    }

    void onEvent(uint8_t type, proto::Reader r, double now) {
        switch (type) {
        case proto::kWelcome: {
            proto::Welcome w;
            if (!proto::decodeWelcome(r, w)) return;
            welcomed = true;
            myId = w.yourId;
            map = w.map;
            break;
        }
        case proto::kPlayerInfo: {
            uint8_t id = r.u8();
            uint8_t len = r.u8();
            std::string n = r.str(len);
            if (!r.bad) names[id] = n;
            break;
        }
        case proto::kPlayerLeft: {
            uint8_t id = r.u8();
            pushFeed(name(id) + " left", now);
            names.erase(id);
            break;
        }
        case proto::kKill: {
            uint8_t killer = r.u8(), victim = r.u8();
            if (r.bad) return;
            pushFeed(name(killer) + "  >  " + name(victim), now);
            if (killer == myId && audio) PlaySound(sndHit);
            break;
        }
        case proto::kRespawn: {
            uint8_t id = r.u8();
            float x = r.f32(), y = r.f32();
            if (!r.bad && id == myId) {
                predX = x; predY = y; meAlive = true;
                pending.clear();
            }
            break;
        }
        default:
            break;
        }
    }

    void pushFeed(const std::string& s, double now) {
        feed.push_back({s, now});
        while (feed.size() > 5) feed.pop_front();
    }

    void handlePacket(const std::vector<uint8_t>& p, double now, double recvTime) {
        lastPacketTime = now;
        uint8_t type = p[0];
        if (type == proto::kSnapshot) {
            proto::Snapshot s;
            if (proto::decodeSnapshot(proto::Reader(p), s)) onSnapshot(s, now);
        } else if (type == proto::kPong) {
            proto::Reader r(p);
            r.u8();
            uint32_t sent = r.u32();
            if (!r.bad) {
                // Microsecond timestamps from the same monotonic clock that
                // stamped the packet on arrival (uint32 wrap-safe).
                double rtt = double(uint32_t(micros32(recvTime) - sent)) / 1000.0;
                pingMs = pingMs < 0 ? rtt : pingMs * 0.8 + rtt * 0.2;
            }
        } else if (type >= proto::kWelcome) {
            proto::Reader r(p);
            r.u8();
            uint16_t seq = r.u16();
            if (r.bad || !conn->acks.receive(seq)) return;  // dedupe resends
            onEvent(type, r, now);
        }
    }

    // Build, predict and send one fixed-step input.
    void sendInput(Vector2 aimWorld, bool focused) {
        proto::Input in;
        in.seq = ++inputSeq;
        if (focused) {
            in.moveX = int8_t((IsKeyDown(KEY_D) || IsKeyDown(KEY_RIGHT)) - (IsKeyDown(KEY_A) || IsKeyDown(KEY_LEFT)));
            in.moveY = int8_t((IsKeyDown(KEY_S) || IsKeyDown(KEY_DOWN)) - (IsKeyDown(KEY_W) || IsKeyDown(KEY_UP)));
            if (IsMouseButtonDown(MOUSE_BUTTON_LEFT)) in.buttons |= proto::kButtonShoot;
        }
        in.aim = std::atan2(aimWorld.y - predY, aimWorld.x - predX);
        in.eventAck = conn->acks.ack;
        in.eventAckBits = conn->acks.bits;
        if (meAlive) {
            game::movePlayer(predX, predY, in.moveX, in.moveY, map);
            pending.push_back(in);
            while (pending.size() > 256) pending.pop_front();
        }
        conn->send(proto::encodeInput(in));
    }

    // Interpolated view of remote players and bullets at renderTime.
    void interpolated(double now, std::vector<proto::PlayerState>& players, std::vector<proto::BulletState>& bullets) const {
        players.clear();
        bullets.clear();
        if (snaps.empty()) return;
        double renderTick = (now - clockOffset - kInterpDelay) * game::kTickRate;
        const proto::Snapshot* a = &snaps.front();
        const proto::Snapshot* b = &snaps.front();
        if (renderTick >= snaps.back().tick) {
            a = b = &snaps.back();
        } else {
            for (size_t i = 0; i + 1 < snaps.size(); i++) {
                if (snaps[i].tick <= renderTick && renderTick <= snaps[i + 1].tick) {
                    a = &snaps[i];
                    b = &snaps[i + 1];
                    break;
                }
            }
        }
        float t = 0;
        if (b->tick != a->tick) t = float((renderTick - a->tick) / double(b->tick - a->tick));
        t = game::clampf(t, 0, 1);
        for (const auto& pb : b->players) {
            proto::PlayerState p = pb;
            for (const auto& pa : a->players) {
                if (pa.id == pb.id && pa.alive && pb.alive) {
                    p.x = pa.x + (pb.x - pa.x) * t;
                    p.y = pa.y + (pb.y - pa.y) * t;
                    p.angle = lerpAngle(pa.angle, pb.angle, t);
                }
            }
            players.push_back(p);
        }
        for (const auto& bb : b->bullets) {
            proto::BulletState s = bb;
            for (const auto& ba : a->bullets)
                if (ba.id == bb.id) { s.x = ba.x + (bb.x - ba.x) * t; s.y = ba.y + (bb.y - ba.y) * t; }
            bullets.push_back(s);
        }
    }
};

void drawPlayer(float x, float y, float angle, const proto::PlayerState& st, const std::string& name, bool me) {
    Color c = playerColor(st.id);
    Vector2 tip{x + std::cos(angle) * (game::kPlayerRadius + 12), y + std::sin(angle) * (game::kPlayerRadius + 12)};
    DrawLineEx({x, y}, tip, 6, DARKGRAY);
    DrawCircleV({x, y}, game::kPlayerRadius, c);
    if (me) DrawCircleLinesV({x, y}, game::kPlayerRadius + 3, RAYWHITE);
    int tw = MeasureText(name.c_str(), 14);
    DrawText(name.c_str(), int(x) - tw / 2, int(y - game::kPlayerRadius - 30), 14, RAYWHITE);
    float frac = float(st.hp) / game::kMaxHP;
    DrawRectangle(int(x - 20), int(y - game::kPlayerRadius - 12), 40, 5, Color{60, 0, 0, 255});
    DrawRectangle(int(x - 20), int(y - game::kPlayerRadius - 12), int(40 * frac), 5, GREEN);
}

}  // namespace

int main(int argc, char** argv) {
    Args args = parseArgs(argc, argv);
    if (!crypto::init()) {
        std::fprintf(stderr, "libsodium init failed\n");
        return 1;
    }
    std::printf("joining %s ...\n", args.server.c_str());
    api::JoinResult jr = api::join(args.server, args.ca, args.name);
    if (!jr.ok) {
        std::fprintf(stderr, "%s\n", jr.error.c_str());
        return 1;
    }
    std::printf("joined as player %d, UDP %s:%d\n", jr.playerId, jr.udpHost.c_str(), jr.udpPort);

    net::Connection conn(jr.token, jr.key);
    std::string err;
    if (!conn.open(jr.udpHost, jr.udpPort, err)) {
        std::fprintf(stderr, "%s\n", err.c_str());
        return 1;
    }
    conn.start();

    SetConfigFlags(FLAG_VSYNC_HINT | FLAG_MSAA_4X_HINT);
    InitWindow(kScreenW, kScreenH, ("Shooter - " + args.name).c_str());
    SetTargetFPS(144);
    SetExitKey(KEY_NULL);  // Esc should not quit mid-fight; close the window instead

    Client cl;
    cl.args = args;
    cl.conn = &conn;
    cl.myId = jr.playerId;
    InitAudioDevice();
    cl.audio = IsAudioDeviceReady();
    if (cl.audio) {
        cl.sndShoot = makeSound(0.08f, 900, 300, 0.2f, 0.25f);
        cl.sndHit = makeSound(0.15f, 500, 900, 0.0f, 0.3f);
        cl.sndDeath = makeSound(0.4f, 200, 60, 0.6f, 0.35f);
    }

    Camera2D cam{};
    cam.offset = {kScreenW / 2.0f, kScreenH / 2.0f};
    cam.zoom = 1;

    const double start = GetTime();
    double lastHello = -1, lastPing = 0, acc = 0, prev = start;
    std::vector<proto::PlayerState> players;
    std::vector<proto::BulletState> bullets;

    while (!WindowShouldClose()) {
        double now = GetTime();
        double dt = now - prev;
        prev = now;

        for (auto& r : conn.drain()) cl.handlePacket(r.payload, now, r.time);

        if (!cl.welcomed && now - lastHello >= kHelloInterval) {
            conn.send({proto::kHello});
            lastHello = now;
        }

        if (cl.welcomed) {
            Vector2 aimWorld = GetScreenToWorld2D(GetMousePosition(), cam);
            acc = std::min(acc + dt, 0.25);
            while (acc >= kInputDt) {
                bool shoot = IsWindowFocused() && IsMouseButtonDown(MOUSE_BUTTON_LEFT);
                if (shoot && cl.meAlive && cl.audio && now - cl.lastShotTime >= double(game::kFireCooldownTicks) / game::kTickRate) {
                    PlaySound(cl.sndShoot);
                    cl.lastShotTime = now;
                }
                cl.sendInput(aimWorld, IsWindowFocused());
                acc -= kInputDt;
            }
            if (now - lastPing >= 1.0) {
                conn.send(proto::encodePing(micros32(net::monoTime())));
                lastPing = now;
            }
        }

        cam.target = {cl.predX, cl.predY};
        cl.interpolated(now, players, bullets);

        BeginDrawing();
        ClearBackground(Color{24, 26, 32, 255});

        if (!cl.welcomed) {
            const char* msg = now - start > kConnectTimeout ? "No response from server (UDP blocked?)" : "Connecting...";
            DrawText(msg, kScreenW / 2 - MeasureText(msg, 28) / 2, kScreenH / 2 - 14, 28, RAYWHITE);
            EndDrawing();
            continue;
        }

        BeginMode2D(cam);
        for (int gx = 0; gx <= int(cl.map.w); gx += 100) DrawLine(gx, 0, gx, int(cl.map.h), Color{40, 44, 52, 255});
        for (int gy = 0; gy <= int(cl.map.h); gy += 100) DrawLine(0, gy, int(cl.map.w), gy, Color{40, 44, 52, 255});
        DrawRectangleLinesEx({0, 0, cl.map.w, cl.map.h}, 4, GRAY);
        for (const auto& w : cl.map.walls) DrawRectangle(w.x, w.y, w.w, w.h, Color{90, 96, 110, 255});
        for (const auto& b : bullets) DrawCircleV({b.x, b.y}, game::kBulletRadius, YELLOW);
        for (const auto& p : players) {
            if (!p.alive) continue;
            if (p.id == cl.myId) {
                Vector2 aim = GetScreenToWorld2D(GetMousePosition(), cam);
                drawPlayer(cl.predX, cl.predY, std::atan2(aim.y - cl.predY, aim.x - cl.predX), p, cl.name(p.id), true);
            } else {
                drawPlayer(p.x, p.y, p.angle, p, cl.name(p.id), false);
            }
        }
        EndMode2D();

        // HUD
        const proto::PlayerState* me = cl.latestMe();
        int hp = me ? me->hp : 0;
        DrawText(TextFormat("HP %d", hp), 20, 20, 28, hp > 25 ? RAYWHITE : RED);
        if (me) DrawText(TextFormat("K %d  D %d", me->kills, me->deaths), 20, 54, 20, LIGHTGRAY);
        DrawText(cl.pingMs < 0 ? "ping --" : TextFormat("ping %.1f ms", cl.pingMs), kScreenW - 130, 20, 20, LIGHTGRAY);
        int fy = 60;
        for (const auto& f : cl.feed) {
            if (now - f.time > 5) continue;
            DrawText(f.text.c_str(), kScreenW - 20 - MeasureText(f.text.c_str(), 18), fy, 18, RAYWHITE);
            fy += 22;
        }
        if (me && !me->alive) {
            const char* msg = "You died - respawning...";
            DrawText(msg, kScreenW / 2 - MeasureText(msg, 32) / 2, kScreenH / 2 - 80, 32, RED);
        }
        if (now - cl.lastPacketTime > kServerSilence) {
            const char* msg = "Connection lost";
            DrawText(msg, kScreenW / 2 - MeasureText(msg, 32) / 2, kScreenH / 2 - 120, 32, ORANGE);
        }
        if (IsKeyDown(KEY_TAB) && !cl.snaps.empty()) {
            auto rows = cl.snaps.back().players;
            std::sort(rows.begin(), rows.end(), [](auto& a, auto& b) { return a.kills != b.kills ? a.kills > b.kills : a.deaths < b.deaths; });
            int h = 50 + int(rows.size()) * 26;
            DrawRectangle(kScreenW / 2 - 200, 120, 400, h, Color{0, 0, 0, 200});
            DrawText("NAME", kScreenW / 2 - 180, 135, 18, GRAY);
            DrawText("K", kScreenW / 2 + 90, 135, 18, GRAY);
            DrawText("D", kScreenW / 2 + 150, 135, 18, GRAY);
            int y = 165;
            for (const auto& r : rows) {
                Color c = r.id == cl.myId ? YELLOW : RAYWHITE;
                DrawText(cl.name(r.id).c_str(), kScreenW / 2 - 180, y, 20, c);
                DrawText(TextFormat("%d", r.kills), kScreenW / 2 + 90, y, 20, c);
                DrawText(TextFormat("%d", r.deaths), kScreenW / 2 + 150, y, 20, c);
                y += 26;
            }
        }
        DrawText("WASD move, mouse aim, LMB shoot, TAB scores", 20, kScreenH - 30, 16, GRAY);
        EndDrawing();
    }

    if (cl.audio) {
        UnloadSound(cl.sndShoot);
        UnloadSound(cl.sndHit);
        UnloadSound(cl.sndDeath);
        CloseAudioDevice();
    }
    conn.stop();
    CloseWindow();
    return 0;
}
