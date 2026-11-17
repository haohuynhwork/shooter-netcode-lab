#include "net.h"

#ifdef _WIN32
#include <winsock2.h>
#include <ws2tcpip.h>
using socklen_t = int;
#else
#include <fcntl.h>
#include <netdb.h>
#include <poll.h>
#include <sys/socket.h>
#include <unistd.h>
#include <cerrno>
#endif

#include <chrono>

namespace net {

double monoTime() {
    using namespace std::chrono;
    return duration<double>(steady_clock::now().time_since_epoch()).count();
}

UdpSocket::~UdpSocket() {
    if (fd_ < 0) return;
#ifdef _WIN32
    closesocket(SOCKET(fd_));
#else
    close(int(fd_));
#endif
}

bool UdpSocket::open(const std::string& host, int port, std::string& err) {
#ifdef _WIN32
    static bool wsa = [] { WSADATA d; return WSAStartup(MAKEWORD(2, 2), &d) == 0; }();
    if (!wsa) { err = "WSAStartup failed"; return false; }
#endif
    addrinfo hints{};
    hints.ai_family = AF_UNSPEC;
    hints.ai_socktype = SOCK_DGRAM;
    addrinfo* res = nullptr;
    std::string portStr = std::to_string(port);
    if (int rc = getaddrinfo(host.c_str(), portStr.c_str(), &hints, &res); rc != 0) {
        err = std::string("resolve ") + host + ": " + gai_strerror(rc);
        return false;
    }
    for (addrinfo* a = res; a; a = a->ai_next) {
        auto s = socket(a->ai_family, a->ai_socktype, a->ai_protocol);
#ifdef _WIN32
        if (s == INVALID_SOCKET) continue;
        if (connect(s, a->ai_addr, int(a->ai_addrlen)) != 0) { closesocket(s); continue; }
        u_long nb = 1;
        ioctlsocket(s, FIONBIO, &nb);
#else
        if (s < 0) continue;
        if (connect(s, a->ai_addr, a->ai_addrlen) != 0) { close(s); continue; }
        fcntl(s, F_SETFL, fcntl(s, F_GETFL) | O_NONBLOCK);
#endif
        fd_ = intptr_t(s);
        break;
    }
    freeaddrinfo(res);
    if (fd_ < 0) err = "could not open UDP socket to " + host;
    return fd_ >= 0;
}

bool UdpSocket::send(const uint8_t* p, size_t n) {
    if (fd_ < 0) return false;
#ifdef _WIN32
    return ::send(SOCKET(fd_), reinterpret_cast<const char*>(p), int(n), 0) == int(n);
#else
    return ::send(int(fd_), p, n, 0) == ssize_t(n);
#endif
}

int UdpSocket::recv(uint8_t* buf, size_t cap) {
    if (fd_ < 0) return -1;
#ifdef _WIN32
    int n = ::recv(SOCKET(fd_), reinterpret_cast<char*>(buf), int(cap), 0);
    if (n < 0) return WSAGetLastError() == WSAEWOULDBLOCK || WSAGetLastError() == WSAECONNRESET ? 0 : -1;
    return n;
#else
    ssize_t n = ::recv(int(fd_), buf, cap, 0);
    if (n < 0) return (errno == EAGAIN || errno == EWOULDBLOCK || errno == ECONNREFUSED) ? 0 : -1;
    return int(n);
#endif
}

bool UdpSocket::wait(int timeoutMs) {
    if (fd_ < 0) return false;
#ifdef _WIN32
    fd_set rs;
    FD_ZERO(&rs);
    FD_SET(SOCKET(fd_), &rs);
    timeval tv{timeoutMs / 1000, (timeoutMs % 1000) * 1000};
    return select(0, &rs, nullptr, nullptr, &tv) > 0;
#else
    pollfd p{int(fd_), POLLIN, 0};
    return ::poll(&p, 1, timeoutMs) > 0;
#endif
}

void Connection::send(const std::vector<uint8_t>& payload) {
    if (payload.size() + crypto::kOverhead > crypto::kMaxPacket) return;
    auto pkt = crypto::seal(key_, crypto::kDirC2S, token_, ++sendCtr_, payload.data(), payload.size());
    sock_.send(pkt.data(), pkt.size());
}

void Connection::start() {
    if (running_.exchange(true)) return;
    thread_ = std::thread([this] { recvLoop(); });
}

void Connection::stop() {
    if (!running_.exchange(false)) return;
    if (thread_.joinable()) thread_.join();
}

void Connection::recvLoop() {
    uint8_t buf[2048];
    std::vector<uint8_t> payload;
    while (running_) {
        if (!sock_.wait(20)) continue;  // short timeout so stop() is prompt
        for (;;) {
            int n = sock_.recv(buf, sizeof buf);
            if (n <= 0) break;
            double t = monoTime();
            uint64_t ctr = crypto::peekCounter(buf, size_t(n));
            if (!replay_.check(ctr)) continue;
            if (!crypto::open(key_, crypto::kDirS2C, buf, size_t(n), ctr, payload)) continue;
            replay_.mark(ctr);
            if (payload.empty()) continue;
            std::lock_guard<std::mutex> lk(mu_);
            queue_.push_back({payload, t});
            if (queue_.size() > 1024) queue_.pop_front();  // main thread stalled
        }
    }
}

std::vector<Received> Connection::drain() {
    std::lock_guard<std::mutex> lk(mu_);
    std::vector<Received> out(std::make_move_iterator(queue_.begin()), std::make_move_iterator(queue_.end()));
    queue_.clear();
    return out;
}

}  // namespace net
