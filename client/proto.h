// Binary protocol, mirrors server/proto.go. Little-endian.
#pragma once
#include <cstdint>
#include <cstring>
#include <string>
#include <vector>

#include "game_shared.h"

namespace proto {

enum : uint8_t {
    kHello = 0,
    kInput = 1,
    kSnapshot = 2,
    kPing = 3,
    kPong = 4,
    kWelcome = 10,
    kKill = 11,
    kRespawn = 12,
    kPlayerInfo = 13,
    kPlayerLeft = 14,
};
constexpr uint8_t kButtonShoot = 1;

struct Writer {
    std::vector<uint8_t> b;
    void u8(uint8_t v) { b.push_back(v); }
    void i8(int8_t v) { b.push_back(uint8_t(v)); }
    void u16(uint16_t v) { for (int i = 0; i < 2; i++) b.push_back(uint8_t(v >> (8 * i))); }
    void u32(uint32_t v) { for (int i = 0; i < 4; i++) b.push_back(uint8_t(v >> (8 * i))); }
    void u64(uint64_t v) { for (int i = 0; i < 8; i++) b.push_back(uint8_t(v >> (8 * i))); }
    void f32(float v) { uint32_t u; std::memcpy(&u, &v, 4); u32(u); }
};

struct Reader {
    const uint8_t* p;
    size_t n;
    bool bad = false;
    Reader(const uint8_t* data, size_t len) : p(data), n(len) {}
    explicit Reader(const std::vector<uint8_t>& v) : p(v.data()), n(v.size()) {}
    bool need(size_t k) {
        if (bad || n < k) { bad = true; return false; }
        return true;
    }
    uint64_t le(size_t k) {
        if (!need(k)) return 0;
        uint64_t v = 0;
        for (size_t i = 0; i < k; i++) v |= uint64_t(p[i]) << (8 * i);
        p += k; n -= k;
        return v;
    }
    uint8_t u8() { return uint8_t(le(1)); }
    int8_t i8() { return int8_t(le(1)); }
    uint16_t u16() { return uint16_t(le(2)); }
    uint32_t u32() { return uint32_t(le(4)); }
    uint64_t u64() { return le(8); }
    float f32() { uint32_t u = u32(); float f; std::memcpy(&f, &u, 4); return f; }
    std::string str(size_t k) {
        if (!need(k)) return {};
        std::string s(reinterpret_cast<const char*>(p), k);
        p += k; n -= k;
        return s;
    }
};

struct Input {
    uint32_t seq = 0;
    int8_t moveX = 0, moveY = 0;
    float aim = 0;
    uint8_t buttons = 0;
    uint16_t eventAck = 0;
    uint32_t eventAckBits = 0;
};

inline std::vector<uint8_t> encodeInput(const Input& in) {
    Writer w;
    w.u8(kInput); w.u32(in.seq); w.i8(in.moveX); w.i8(in.moveY);
    w.f32(in.aim); w.u8(in.buttons); w.u16(in.eventAck); w.u32(in.eventAckBits);
    return w.b;
}

inline std::vector<uint8_t> encodePing(uint32_t t) {
    Writer w;
    w.u8(kPing); w.u32(t);
    return w.b;
}

struct PlayerState {
    uint8_t id = 0;
    float x = 0, y = 0, angle = 0;
    uint8_t hp = 0;
    bool alive = false;
    uint16_t kills = 0, deaths = 0;
};

struct BulletState {
    uint16_t id = 0;
    float x = 0, y = 0;
};

struct Snapshot {
    uint32_t tick = 0, lastAckedSeq = 0;
    std::vector<PlayerState> players;
    std::vector<BulletState> bullets;
};

inline bool decodeSnapshot(Reader r, Snapshot& s) {
    if (r.u8() != kSnapshot) return false;
    s.tick = r.u32();
    s.lastAckedSeq = r.u32();
    int n = r.u8();
    s.players.clear();
    for (int i = 0; i < n && !r.bad; i++) {
        PlayerState p;
        p.id = r.u8(); p.x = r.f32(); p.y = r.f32(); p.angle = r.f32();
        p.hp = r.u8(); p.alive = r.u8() != 0; p.kills = r.u16(); p.deaths = r.u16();
        s.players.push_back(p);
    }
    n = r.u8();
    s.bullets.clear();
    for (int i = 0; i < n && !r.bad; i++) {
        BulletState b;
        b.id = r.u16(); b.x = r.f32(); b.y = r.f32();
        s.bullets.push_back(b);
    }
    return !r.bad;
}

struct Welcome {
    uint8_t yourId = 0;
    game::Map map;
};

inline bool decodeWelcome(Reader& r, Welcome& w) {
    w.yourId = r.u8();
    w.map.w = r.u16();
    w.map.h = r.u16();
    int n = r.u8();
    for (int i = 0; i < n && !r.bad; i++) {
        game::Wall wl;
        wl.x = r.u16(); wl.y = r.u16(); wl.w = r.u16(); wl.h = r.u16();
        w.map.walls.push_back(wl);
    }
    return !r.bad;
}

}  // namespace proto
