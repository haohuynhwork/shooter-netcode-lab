// Gameplay constants and movement, mirrored from server/game.go.
// MovePlayer must stay bit-identical to the server for prediction to match.
#pragma once
#include <cstdint>
#include <vector>

namespace game {

constexpr int kTickRate = 30;
constexpr int kInputRate = 60;
constexpr float kPlayerRadius = 16.0f;
constexpr float kPlayerSpeed = 220.0f;
constexpr float kBulletRadius = 4.0f;
constexpr int kMaxHP = 100;
constexpr int kFireCooldownTicks = 6;
constexpr float kDiag = 0.70710678f;
constexpr float kInputStep = kPlayerSpeed / kInputRate;

struct Wall {
    uint16_t x, y, w, h;
};

struct Map {
    float w = 2000, h = 1500;
    std::vector<Wall> walls;
};

inline float clampf(float v, float lo, float hi) { return v < lo ? lo : (v > hi ? hi : v); }

inline bool circleHitsRect(float cx, float cy, float r, const Wall& w) {
    float nx = clampf(cx, float(w.x), float(w.x) + float(w.w));
    float ny = clampf(cy, float(w.y), float(w.y) + float(w.h));
    float dx = cx - nx, dy = cy - ny;
    return dx * dx + dy * dy < r * r;
}

inline bool hitsAnyWall(float x, float y, float r, const std::vector<Wall>& walls) {
    for (const auto& w : walls)
        if (circleHitsRect(x, y, r, w)) return true;
    return false;
}

inline void movePlayer(float& x, float& y, int8_t mx, int8_t my, const Map& m) {
    float dx = float(mx), dy = float(my);
    if (dx != 0 && dy != 0) {
        dx *= kDiag;
        dy *= kDiag;
    }
    float nx = clampf(x + dx * kInputStep, kPlayerRadius, m.w - kPlayerRadius);
    if (!hitsAnyWall(nx, y, kPlayerRadius, m.walls)) x = nx;
    float ny = clampf(y + dy * kInputStep, kPlayerRadius, m.h - kPlayerRadius);
    if (!hitsAnyWall(x, ny, kPlayerRadius, m.walls)) y = ny;
}

}  // namespace game
