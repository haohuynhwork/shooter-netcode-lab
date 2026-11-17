package match

import (
	"math"
	"testing"
)

func TestHitKillRespawnEvents(t *testing.T) {
	m := New(nil, 1)
	if _, err := m.Join(1, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Join(1, "a"); err != ErrAlreadyJoined {
		t.Fatal("double join allowed")
	}
	m.Join(2, "b")
	m.players[1].X, m.players[1].Y = 100, 100
	m.players[2].X, m.players[2].Y = 300, 100

	var killed *PlayerKilled
	for seq := uint32(1); seq < 200 && killed == nil; seq++ {
		m.QueueInput(1, Input{Seq: seq, Shoot: true})
		for _, e := range m.Step() {
			if k, ok := e.(PlayerKilled); ok {
				killed = &k
			}
		}
	}
	if killed == nil || *killed != (PlayerKilled{Killer: 1, Victim: 2}) {
		t.Fatalf("kill event %+v", killed)
	}
	a, _ := m.Player(1)
	b, _ := m.Player(2)
	if b.Alive || b.Deaths != 1 || a.Kills != 1 || a.HP != MaxHP {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
	respawned := false
	for i := 0; i <= RespawnTicks && !respawned; i++ {
		for _, e := range m.Step() {
			if r, ok := e.(PlayerRespawned); ok && r.ID == 2 {
				respawned = true
			}
		}
	}
	if b, _ = m.Player(2); !respawned || !b.Alive || b.HP != MaxHP {
		t.Fatal("no respawn")
	}
	if ev := m.Leave(2); len(ev) != 1 || ev[0] != (PlayerLeft{ID: 2}) {
		t.Fatalf("leave %+v", ev)
	}
}

func TestMovementClampAndWalls(t *testing.T) {
	x, y := MovePlayer(PlayerRadius, PlayerRadius, -1, -1, nil)
	if x != PlayerRadius || y != PlayerRadius {
		t.Fatalf("not clamped: %v %v", x, y)
	}
	w := []Wall{{100, 0, 40, 200}}
	x, y = float32(100-PlayerRadius-1), 50
	for i := 0; i < 10; i++ {
		x, y = MovePlayer(x, y, 1, 0, w)
	}
	if x >= 100-PlayerRadius {
		t.Fatalf("walked through wall: %v", x)
	}
	x, y = MovePlayer(1000, 1000, 1, 1, nil)
	if d := math.Hypot(float64(x-1000), float64(y-1000)); math.Abs(d-float64(InputStep)) > 1e-3 {
		t.Fatalf("diag step %v", d)
	}
}

func TestInputValidation(t *testing.T) {
	m := New(nil, 1)
	m.Join(1, "a")
	m.QueueInput(1, Input{Seq: 1, MoveX: 100, MoveY: -100, Aim: float32(math.NaN())})
	if in := m.players[1].inputs[0]; in.MoveX != 1 || in.MoveY != -1 || in.Aim != 0 {
		t.Fatalf("not sanitized: %+v", in)
	}
	if m.QueueInput(1, Input{Seq: 1}) {
		t.Fatal("duplicate seq accepted")
	}
	if m.QueueInput(9, Input{Seq: 1}) {
		t.Fatal("unknown player accepted")
	}
	for i := uint32(2); i < 50; i++ {
		m.QueueInput(1, Input{Seq: i})
	}
	if len(m.players[1].inputs) != MaxQueuedInputs {
		t.Fatalf("queue %d", len(m.players[1].inputs))
	}
	m.Step()
	if p, _ := m.Player(1); p.LastSeq != 49-MaxQueuedInputs+MaxInputsPerTick {
		t.Fatalf("LastSeq %d", p.LastSeq)
	}
}
