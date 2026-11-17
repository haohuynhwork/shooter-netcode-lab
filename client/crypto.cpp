#include "crypto.h"

#include <sodium.h>

namespace crypto {

static void putLE(uint8_t* p, uint64_t v, int n) {
    for (int i = 0; i < n; i++) p[i] = uint8_t(v >> (8 * i));
}

static uint64_t getLE(const uint8_t* p, int n) {
    uint64_t v = 0;
    for (int i = 0; i < n; i++) v |= uint64_t(p[i]) << (8 * i);
    return v;
}

static void makeNonce(uint8_t nonce[12], uint32_t dir, uint64_t ctr) {
    putLE(nonce, dir, 4);
    putLE(nonce + 4, ctr, 8);
}

bool init() { return sodium_init() >= 0; }

std::vector<uint8_t> seal(const Key& key, uint32_t dir, uint64_t token, uint64_t ctr,
                          const uint8_t* payload, size_t len) {
    std::vector<uint8_t> out(kHeader + len + kTag);
    putLE(out.data(), token, 8);
    putLE(out.data() + 8, ctr, 8);
    uint8_t nonce[12];
    makeNonce(nonce, dir, ctr);
    unsigned long long clen = 0;
    crypto_aead_chacha20poly1305_ietf_encrypt(out.data() + kHeader, &clen, payload, len,
                                              out.data(), kHeader, nullptr, nonce, key.data());
    out.resize(kHeader + size_t(clen));
    return out;
}

bool open(const Key& key, uint32_t dir, const uint8_t* pkt, size_t len, uint64_t& ctrOut,
          std::vector<uint8_t>& payloadOut) {
    if (len < kOverhead || len > kMaxPacket) return false;
    uint64_t ctr = getLE(pkt + 8, 8);
    if (ctr == 0) return false;
    uint8_t nonce[12];
    makeNonce(nonce, dir, ctr);
    payloadOut.resize(len - kOverhead);
    unsigned long long mlen = 0;
    if (crypto_aead_chacha20poly1305_ietf_decrypt(payloadOut.data(), &mlen, nullptr,
                                                  pkt + kHeader, len - kHeader, pkt, kHeader,
                                                  nonce, key.data()) != 0)
        return false;
    payloadOut.resize(size_t(mlen));
    ctrOut = ctr;
    return true;
}

uint64_t peekCounter(const uint8_t* pkt, size_t len) {
    return len < kHeader ? 0 : getLE(pkt + 8, 8);
}

}  // namespace crypto
