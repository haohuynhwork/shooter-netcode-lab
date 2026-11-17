#include "api.h"

#define CPPHTTPLIB_OPENSSL_SUPPORT
#include <httplib.h>
#include <sodium.h>

#include <cstdlib>

namespace api {

// Minimal extractor for the flat JSON /join returns: string or number values.
std::string jsonField(const std::string& json, const std::string& key) {
    std::string pat = "\"" + key + "\"";
    size_t p = json.find(pat);
    if (p == std::string::npos) return {};
    p = json.find(':', p + pat.size());
    if (p == std::string::npos) return {};
    p = json.find_first_not_of(" \t\r\n", p + 1);
    if (p == std::string::npos) return {};
    if (json[p] == '"') {
        size_t e = json.find('"', p + 1);
        return e == std::string::npos ? std::string{} : json.substr(p + 1, e - p - 1);
    }
    size_t e = json.find_first_of(",} \t\r\n", p);
    return json.substr(p, e == std::string::npos ? std::string::npos : e - p);
}

bool splitHostPort(const std::string& addr, std::string& host, int& port) {
    size_t c = addr.rfind(':');
    if (c == std::string::npos || c + 1 >= addr.size()) return false;
    host = addr.substr(0, c);
    if (host.size() >= 2 && host.front() == '[' && host.back() == ']') host = host.substr(1, host.size() - 2);
    char* end = nullptr;
    long p = std::strtol(addr.c_str() + c + 1, &end, 10);
    if (*end != '\0' || p <= 0 || p > 65535) return false;
    port = int(p);
    return !host.empty();
}

static std::string jsonEscape(const std::string& s) {
    std::string o;
    for (char ch : s) {
        if (ch == '"' || ch == '\\') { o += '\\'; o += ch; }
        else if (static_cast<unsigned char>(ch) >= 0x20 && static_cast<unsigned char>(ch) < 0x7f) o += ch;
    }
    return o;
}

JoinResult join(const std::string& baseUrl, const std::string& caFile, const std::string& name) {
    JoinResult r;
    if (baseUrl.rfind("https://", 0) != 0) {
        r.error = "server URL must start with https://";
        return r;
    }
    httplib::Client cli(baseUrl);
    cli.set_ca_cert_path(caFile.c_str());
    cli.enable_server_certificate_verification(true);
    cli.set_connection_timeout(5);
    cli.set_read_timeout(5);

    auto res = cli.Post("/join", "{\"name\":\"" + jsonEscape(name) + "\"}", "application/json");
    if (!res) {
        r.error = "join failed: " + httplib::to_string(res.error());
        if (res.error() == httplib::Error::SSLServerVerification)
            r.error += " (is --ca pointing at the server's certs/server.crt?)";
        return r;
    }
    if (res->status != 200) {
        r.error = "join rejected: HTTP " + std::to_string(res->status) + " " + res->body;
        return r;
    }
    const std::string& body = res->body;

    std::string tok = jsonField(body, "token");
    char* end = nullptr;
    r.token = std::strtoull(tok.c_str(), &end, 16);
    if (tok.size() != 16 || *end != '\0') { r.error = "bad token in response"; return r; }

    std::string key = jsonField(body, "key");
    size_t keyLen = 0;
    if (sodium_base642bin(r.key.data(), r.key.size(), key.c_str(), key.size(), nullptr, &keyLen,
                          nullptr, sodium_base64_VARIANT_ORIGINAL) != 0 ||
        keyLen != r.key.size()) {
        r.error = "bad key in response";
        return r;
    }

    r.playerId = std::atoi(jsonField(body, "playerId").c_str());
    if (!splitHostPort(jsonField(body, "udpAddr"), r.udpHost, r.udpPort)) {
        r.error = "bad udpAddr in response";
        return r;
    }
    r.ok = true;
    return r;
}

}  // namespace api
