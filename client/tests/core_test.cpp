// Tests for the client core. The crypto vectors are shared with
// server/crypto_test.go, so Go and C++ must produce identical bytes.
#include <cmath>
#include <cstdio>
#include <set>
#include <string>

#include "api.h"
#include "crypto.h"
#include "game_shared.h"
#include "net.h"
#include "proto.h"

static int failures = 0;
#define CHECK(c)                                                         \
    do {                                                                 \
        if (!(c)) {                                                      \
            std::printf("FAIL %s:%d: %s\n", __FILE__, __LINE__, #c);     \
            failures++;                                                  \
        }                                                                \
    } while (0)

static std::string hex(const std::vector<uint8_t>& v) {
    static const char* d = "0123456789abcdef";
    std::string s;
    for (uint8_t b : v) { s += d[b >> 4]; s += d[b & 15]; }
    return s;
}

static crypto::Key testKey() {
    crypto::Key k;
    for (int i = 0; i < 32; i++) k[i] = uint8_t(i);
    return k;
}

static void testCryptoVectors() {
    auto k = testKey();
    const uint8_t msg[] = {'h', 'e', 'l', 'l', 'o'};
    CHECK(hex(crypto::seal(k, crypto::kDirC2S, 0x0102030405060708ULL, 1, msg, 5)) ==
          "08070605040302010100000000000000f7329f33daea476d8ac19392d92f14985db32b86d2");
    CHECK(hex(crypto::seal(k, crypto::kDirS2C, 0x0102030405060708ULL, 7, msg, 5)) ==
          "08070605040302010700000000000000120b81c85c38db5851fa6f464021decef5a0486e6d");
}

static void testCryptoRejects() {
    auto k = testKey();
    const uint8_t msg[] = {1, 2, 3};
    auto pkt = crypto::seal(k, crypto::kDirS2C, 42, 9, msg, 3);
    uint64_t ctr = 0;
    std::vector<uint8_t> out;
    CHECK(crypto::open(k, crypto::kDirS2C, pkt.data(), pkt.size(), ctr, out) && ctr == 9 && out.size() == 3 && out[2] == 3);
    CHECK(!crypto::open(k, crypto::kDirC2S, pkt.data(), pkt.size(), ctr, out));
    for (size_t i = 0; i < pkt.size(); i++) {
        auto bad = pkt;
        bad[i] ^= 1;
        CHECK(!crypto::open(k, crypto::kDirS2C, bad.data(), bad.size(), ctr, out));
    }
    auto k2 = k;
    k2[0] ^= 0xff;
    CHECK(!crypto::open(k2, crypto::kDirS2C, pkt.data(), pkt.size(), ctr, out));
}

static void testReplayWindow() {
    crypto::ReplayWindow w;
    auto accept = [&](uint64_t c) { if (!w.check(c)) return false; w.mark(c); return true; };
    CHECK(accept(1) && accept(2) && accept(5) && accept(3));
    CHECK(!accept(0) && !accept(1) && !accept(5));
    CHECK(accept(4));
    CHECK(accept(100));
    CHECK(!accept(36));
    CHECK(accept(37) && !accept(37));
}

static void testAckTracker() {
    net::AckTracker t;
    CHECK(t.receive(1) && t.receive(2) && t.receive(4));
    CHECK(!t.receive(2));
    CHECK(t.ack == 4 && t.bits == 0b110);  // 3 missing, 2 and 1 present
    CHECK(t.receive(3) && !t.receive(3));
    CHECK(t.receive(36) && t.ack == 36 && t.bits == uint32_t(1) << 31);  // only 4 still in window
    net::AckTracker w;  // wraparound
    CHECK(w.receive(65535) && w.receive(0) && w.receive(1) && !w.receive(65535));
    CHECK(w.ack == 1 && (w.bits & 0b11) == 0b11);
}

static void testProtoAndMove() {
    proto::Input in;
    in.seq = 77; in.moveX = -1; in.moveY = 1; in.aim = 1.25f; in.buttons = 1;
    in.eventAck = 9; in.eventAckBits = 0xdeadbeef;
    auto enc = proto::encodeInput(in);
    CHECK(enc.size() == 18);
    CHECK(hex(enc) == "014d000000ff010000a03f010900efbeadde");

    proto::Writer w;
    w.u8(proto::kSnapshot); w.u32(5); w.u32(4); w.u8(1);
    w.u8(3); w.f32(10); w.f32(20); w.f32(0.5f); w.u8(75); w.u8(1); w.u16(2); w.u16(1);
    w.u8(1); w.u16(9); w.f32(1.5f); w.f32(2.5f);
    proto::Snapshot s;
    CHECK(proto::decodeSnapshot(proto::Reader(w.b), s));
    CHECK(s.tick == 5 && s.players.size() == 1 && s.players[0].hp == 75 && s.players[0].alive && s.bullets[0].y == 2.5f);
    w.b.pop_back();
    CHECK(!proto::decodeSnapshot(proto::Reader(w.b), s));

    game::Map m;
    float x = game::kPlayerRadius, y = game::kPlayerRadius;
    game::movePlayer(x, y, -1, -1, m);
    CHECK(x == game::kPlayerRadius && y == game::kPlayerRadius);
    x = 1000; y = 1000;
    game::movePlayer(x, y, 1, 1, m);
    CHECK(std::fabs(std::hypot(x - 1000, y - 1000) - game::kInputStep) < 1e-3);
}

static void testApiHelpers() {
    std::string j = R"({"token":"00ff00ff00ff00ff","key":"abc=","playerId":3,"udpAddr":"[::1]:7777"})";
    CHECK(api::jsonField(j, "token") == "00ff00ff00ff00ff");
    CHECK(api::jsonField(j, "playerId") == "3");
    std::string h;
    int p = 0;
    CHECK(api::splitHostPort(api::jsonField(j, "udpAddr"), h, p) && h == "::1" && p == 7777);
    CHECK(api::splitHostPort("localhost:1", h, p) && h == "localhost" && p == 1);
    CHECK(!api::splitHostPort("localhost", h, p));
}

int main() {
    if (!crypto::init()) { std::puts("sodium_init failed"); return 1; }
    testCryptoVectors();
    testCryptoRejects();
    testReplayWindow();
    testAckTracker();
    testProtoAndMove();
    testApiHelpers();
    if (failures == 0) std::puts("all client core tests passed");
    return failures == 0 ? 0 : 1;
}
