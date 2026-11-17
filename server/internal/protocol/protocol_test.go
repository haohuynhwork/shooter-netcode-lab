package protocol

import (
	"math/rand/v2"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	in := Input{Seq: 77, MoveX: -1, MoveY: 1, Aim: 1.25, Buttons: ButtonShoot, EventAck: 9, EventAckBits: 0xdeadbeef}
	enc := EncodeInput(in)
	if len(enc) != 18 {
		t.Fatalf("input size %d", len(enc))
	}
	if got, err := DecodeInput(enc); err != nil || got != in {
		t.Fatalf("input: %v %+v", err, got)
	}
	if _, err := DecodeInput(enc[:10]); err == nil {
		t.Fatal("short input accepted")
	}

	s := Snapshot{Tick: 5, LastAckedSeq: 4,
		Players: []PlayerState{{ID: 1, X: 10, Y: 20, Angle: 0.5, HP: 75, Alive: true, Kills: 3, Deaths: 2}, {ID: 2}},
		Bullets: []BulletState{{ID: 9, X: 1.5, Y: 2.5}},
	}
	enc = EncodeSnapshot(s)
	if len(enc) != SnapshotHeaderSize+2*PlayerStateSize+BulletStateSize {
		t.Fatalf("snapshot size %d", len(enc))
	}
	gs, err := DecodeSnapshot(enc)
	if err != nil || gs.Tick != 5 || len(gs.Players) != 2 || gs.Players[0] != s.Players[0] || gs.Bullets[0] != s.Bullets[0] {
		t.Fatalf("snapshot: %v %+v", err, gs)
	}

	walls := []Wall{{1, 2, 3, 4}, {5, 6, 7, 8}}
	wc, err := DecodeWelcomeBody(WelcomeBody(3, 2000, 1500, walls))
	if err != nil || wc.YourID != 3 || wc.MapW != 2000 || len(wc.Walls) != 2 || wc.Walls[1] != walls[1] {
		t.Fatalf("welcome: %v %+v", err, wc)
	}

	if v, err := DecodePing(PingPayload(MsgPing, 12345)); err != nil || v != 12345 {
		t.Fatalf("ping %v %v", v, err)
	}
	if _, err := DecodePing([]byte{MsgPing, 1, 2}); err == nil {
		t.Fatal("short ping accepted")
	}
}

func TestOutboxAckTracker(t *testing.T) {
	o := NewOutbox()
	var tr AckTracker
	now := time.Unix(0, 0)
	for i := 0; i < 5; i++ {
		o.Push(MsgKill, KillBody(1, 2))
	}
	due := o.Due(now, 100*time.Millisecond)
	if len(due) != 5 || len(o.Due(now.Add(50*time.Millisecond), 100*time.Millisecond)) != 0 {
		t.Fatal("due/resend timing")
	}
	for _, i := range []int{0, 1, 3} { // 3 and 5 lost
		_, seq, _, _ := DecodeEventHeader(due[i])
		if !tr.Receive(seq) {
			t.Fatalf("seq %d not new", seq)
		}
	}
	if tr.Receive(2) {
		t.Fatal("duplicate accepted")
	}
	o.Ack(tr.Ack, tr.Bits)
	if o.Len() != 2 {
		t.Fatalf("pending %d, want 2", o.Len())
	}
	for _, p := range o.Due(now.Add(100*time.Millisecond), 100*time.Millisecond) {
		_, seq, _, _ := DecodeEventHeader(p)
		tr.Receive(seq)
	}
	o.Ack(tr.Ack, tr.Bits)
	if o.Len() != 0 {
		t.Fatalf("pending %d", o.Len())
	}
}

// 5% loss both ways, across the u16 wrap: every event arrives exactly once.
func TestReliableUnderLoss(t *testing.T) {
	o := NewOutbox()
	o.next = 65000
	var tr AckTracker
	rng := rand.New(rand.NewPCG(1, 2))
	got := map[string]int{}
	now := time.Unix(0, 0)
	sent := 0
	for step := 0; step < 3000; step++ {
		if step%3 == 0 && sent < 1000 {
			o.Push(MsgPlayerInfo, PlayerInfoBody(1, string(rune('a'+sent%26))+string(rune(sent))))
			sent++
		}
		now = now.Add(time.Second / 30)
		for _, p := range o.Due(now, 100*time.Millisecond) {
			if rng.Float64() < 0.05 {
				continue
			}
			_, seq, body, _ := DecodeEventHeader(p)
			if tr.Receive(seq) {
				got[string(body)]++
			}
		}
		if rng.Float64() >= 0.05 {
			o.Ack(tr.Ack, tr.Bits)
		}
	}
	if len(got) != 1000 || o.Len() != 0 {
		t.Fatalf("delivered %d pending %d", len(got), o.Len())
	}
	for k, n := range got {
		if n != 1 {
			t.Fatalf("event %q delivered %d times", k, n)
		}
	}
}
