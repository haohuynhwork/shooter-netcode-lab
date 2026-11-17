// HTTPS /join: gets the session token and key before any UDP traffic.
#pragma once
#include <string>

#include "crypto.h"

namespace api {

struct JoinResult {
    bool ok = false;
    std::string error;
    uint64_t token = 0;
    crypto::Key key{};
    int playerId = -1;
    std::string udpHost;
    int udpPort = 0;
};

// baseUrl like "https://localhost:8443"; caFile is the server's dev cert.
JoinResult join(const std::string& baseUrl, const std::string& caFile, const std::string& name);

// Exposed for tests.
std::string jsonField(const std::string& json, const std::string& key);
bool splitHostPort(const std::string& addr, std::string& host, int& port);

}  // namespace api
