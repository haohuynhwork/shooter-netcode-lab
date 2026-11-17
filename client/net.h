// UDP socket + encrypted connection + reliable-event dedupe.
#pragma once
#include <atomic>
#include <cstdint>
#include <deque>
#include <mutex>
#include <thread>
#include <string>
#include <vector>

#include "crypto.h"

namespace net {

// Receiving side of reliable events, mirrors AckTracker in server/reliable.go.
// bits: bit i => (ack - 1 - i) received.
struct AckTracker {
    static constexpr int kWindow = 32;
    uint16_t ack = 0;
    uint32_t bits = 0;
    bool any = false;

    bool receive(uint16_t seq) {
        if (!any) {
            any = true; ack = seq; bits = 0;
            return true;
        }
        int d = int16_t(uint16_t(seq - ack));
        if (d == 0) return false;
        if (d > 0) {
            if (d >= kWindow) bits = d == kWindow ? (uint32_t(1) << 31) : 0;
            else bits = (bits << d) | (uint32_t(1) << (d - 1));
            ack = seq;
            return true;
        }
        int k = -d;
        if (k > kWindow) return false;
        uint32_t m = uint32_t(1) << (k - 1);
        if (bits & m) return false;
        bits |= m;
        return true;
    }
};

class UdpSocket {
public:
    UdpSocket() = default;
    ~UdpSocket();
    UdpSocket(const UdpSocket&) = delete;
    UdpSocket& operator=(const UdpSocket&) = delete;

    // Resolves host and connects (UDP) so send/recv need no address. Non-blocking.
    bool open(const std::string& host, int port, std::string& err);
    bool send(const uint8_t* p, size_t n);
    // Returns bytes read, 0 if nothing pending, -1 on error.
    int recv(uint8_t* buf, size_t cap);
    // Blocks until readable or timeout. Returns true if readable.
    bool wait(int timeoutMs);

private:
    intptr_t fd_ = -1;
};

// Monotonic seconds; used to timestamp packets on arrival.
double monoTime();

struct Received {
    std::vector<uint8_t> payload;
    double time;  // monoTime() when the packet was read off the socket
};

// Receiving runs on a background thread so arrival times are not quantized
// to the render frame. Sending and AckTracker stay on the main thread.
class Connection {
public:
    Connection(uint64_t token, const crypto::Key& key) : token_(token), key_(key) {}
    ~Connection() { stop(); }
    bool open(const std::string& host, int port, std::string& err) { return sock_.open(host, port, err); }

    void start();
    void stop();
    void send(const std::vector<uint8_t>& payload);
    // Returns authentic, non-replayed payloads received since the last call.
    std::vector<Received> drain();

    AckTracker acks;

private:
    void recvLoop();

    UdpSocket sock_;
    uint64_t token_;
    crypto::Key key_;
    uint64_t sendCtr_ = 0;
    crypto::ReplayWindow replay_;  // receive thread only

    std::thread thread_;
    std::atomic<bool> running_{false};
    std::mutex mu_;
    std::deque<Received> queue_;
};

}  // namespace net
