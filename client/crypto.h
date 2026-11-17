// Encrypted UDP envelope, mirrors server/crypto.go:
//   u64 token | u64 counter | ChaCha20-Poly1305(payload) | 16-byte tag
// Nonce = u32 direction | u64 counter (LE). Header is associated data.
#pragma once
#include <array>
#include <cstddef>
#include <cstdint>
#include <vector>

namespace crypto {

constexpr size_t kHeader = 16;
constexpr size_t kTag = 16;
constexpr size_t kOverhead = kHeader + kTag;
constexpr size_t kMaxPacket = 1200;
constexpr uint32_t kDirC2S = 0;
constexpr uint32_t kDirS2C = 1;

using Key = std::array<uint8_t, 32>;

bool init();  // sodium_init; call once at startup

std::vector<uint8_t> seal(const Key& key, uint32_t dir, uint64_t token, uint64_t ctr,
                          const uint8_t* payload, size_t len);

// Authenticates and decrypts. Does not check replays (see ReplayWindow).
bool open(const Key& key, uint32_t dir, const uint8_t* pkt, size_t len,
          uint64_t& ctrOut, std::vector<uint8_t>& payloadOut);

uint64_t peekCounter(const uint8_t* pkt, size_t len);

// Accepts each counter once and nothing more than 63 behind the highest seen.
struct ReplayWindow {
    uint64_t max = 0, bits = 0;
    bool check(uint64_t c) const {
        if (c == 0) return false;
        if (c > max) return true;
        uint64_t d = max - c;
        return d < 64 && !(bits & (uint64_t(1) << d));
    }
    void mark(uint64_t c) {
        if (c > max) {
            uint64_t s = c - max;
            bits = s >= 64 ? 1 : (bits << s) | 1;
            max = c;
        } else {
            bits |= uint64_t(1) << (max - c);
        }
    }
};

}  // namespace crypto
