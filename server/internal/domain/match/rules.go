// Package match is the domain core: the rules of a deathmatch. It knows
// nothing about networking, encoding, encryption or time sources.
package match

// Game rules. Movement values are mirrored in client/game_shared.h so the
// client's prediction matches the server.
const (
	TickRate                  = 30
	InputRate                 = 60 // one Input moves a player for 1/60 s
	MapW                      = 2000
	MapH                      = 1500
	PlayerRadius      float32 = 16
	PlayerSpeed       float32 = 220 // px/s
	BulletSpeed       float32 = 700 // px/s
	BulletRadius      float32 = 4
	BulletLifeTicks           = 36 // 1.2 s
	Damage                    = 25
	MaxHP                     = 100
	FireCooldownTicks         = 6  // 5 shots/s
	RespawnTicks              = 90 // 3 s
	MaxInputsPerTick          = 4  // 2 expected; allows catch-up, caps speed hacks
	MaxQueuedInputs           = 8

	diag      float32 = 0.70710678
	InputStep float32 = PlayerSpeed / InputRate
)

// Wall is an axis-aligned obstacle (value object).
type Wall struct{ X, Y, W, H uint16 }

var DefaultWalls = []Wall{
	{300, 300, 400, 40}, {300, 300, 40, 300},
	{1300, 300, 400, 40}, {1660, 300, 40, 300},
	{300, 1160, 400, 40}, {300, 900, 40, 300},
	{1300, 1160, 400, 40}, {1660, 900, 40, 300},
	{900, 650, 200, 200},
	{700, 60, 40, 250}, {1260, 1190, 40, 250},
}

func clampf(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func sq(v float32) float32 { return v * v }

func circleHitsRect(cx, cy, r float32, w Wall) bool {
	nx := clampf(cx, float32(w.X), float32(w.X)+float32(w.W))
	ny := clampf(cy, float32(w.Y), float32(w.Y)+float32(w.H))
	return sq(cx-nx)+sq(cy-ny) < r*r
}

func HitsAnyWall(x, y, r float32, walls []Wall) bool {
	for _, w := range walls {
		if circleHitsRect(x, y, r, w) {
			return true
		}
	}
	return false
}

// MovePlayer applies one input step. Must stay bit-identical to the client.
func MovePlayer(x, y float32, mx, my int8, walls []Wall) (float32, float32) {
	dx, dy := float32(mx), float32(my)
	if dx != 0 && dy != 0 {
		dx *= diag
		dy *= diag
	}
	// Explicit conversions stop FMA fusion so results match the C++ client.
	nx := clampf(x+float32(dx*InputStep), PlayerRadius, MapW-PlayerRadius)
	if !HitsAnyWall(nx, y, PlayerRadius, walls) {
		x = nx
	}
	ny := clampf(y+float32(dy*InputStep), PlayerRadius, MapH-PlayerRadius)
	if !HitsAnyWall(x, ny, PlayerRadius, walls) {
		y = ny
	}
	return x, y
}
