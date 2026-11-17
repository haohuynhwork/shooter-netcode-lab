package app

import (
	"testing"
	"time"

	"shooter/server/internal/domain/match"
	"shooter/server/internal/protocol"
)

type fakeTransport struct{ sent map[uint8][][]byte }

func (f *fakeTransport) Send(id uint8, p []byte) {
	if f.sent == nil {
		f.sent = map[uint8][][]byte{}
	}
	f.sent[id] = append(f.sent[id], p)
}

func (f *fakeTransport) events(id uint8) map[uint8]int {
	n := map[uint8]int{}
	for _, p := range f.sent[id] {
		if p[0] >= protocol.MsgWelcome {
			n[p[0]]++
		}
	}
	return n
}

const maxPayload = 1200 - 32

func TestJoinSendsWelcomeAndNames(t *testing.T) {
	ft := &fakeTransport{}
	s := New(match.New(match.DefaultWalls, 1), ft, maxPayload)
	s.Handle(Inbound{ID: 1, Token: 11, Name: "a", Payload: []byte{protocol.MsgHello}})
	s.Handle(Inbound{ID: 2, Token: 22, Name: "b", Payload: []byte{protocol.MsgHello}})
	s.Handle(Inbound{ID: 2, Token: 22, Name: "b", Payload: []byte{protocol.MsgHello}}) // repeat HELLO: no-op
	s.Tick(time.Unix(0, 0))

	// a: WELCOME, info(a), info(b). b: WELCOME, info(a) (existing), info(b).
	for _, id := range []uint8{1, 2} {
		ev := ft.events(id)
		if ev[protocol.MsgWelcome] != 1 || ev[protocol.MsgPlayerInfo] != 2 {
			t.Fatalf("player %d events %v", id, ev)
		}
	}
	snap, err := protocol.DecodeSnapshot(ft.sent[1][0])
	if err != nil || len(snap.Players) != 2 {
		t.Fatalf("snapshot %v %+v", err, snap)
	}
}

func TestWrongTokenIgnored(t *testing.T) {
	m := match.New(nil, 1)
	s := New(m, &fakeTransport{}, maxPayload)
	s.Handle(Inbound{ID: 1, Token: 11, Payload: []byte{protocol.MsgHello}})
	s.Handle(Inbound{ID: 1, Token: 99, Payload: protocol.EncodeInput(protocol.Input{Seq: 5, MoveX: 1})})
	before, _ := m.Player(1)
	s.Tick(time.Unix(0, 0))
	if after, _ := m.Player(1); after.X != before.X || after.LastSeq != 0 {
		t.Fatal("input with wrong token applied")
	}
}

func TestDomainEventsBroadcastReliably(t *testing.T) {
	ft := &fakeTransport{}
	s := New(match.New(nil, 1), ft, maxPayload)
	s.Handle(Inbound{ID: 1, Token: 11, Payload: []byte{protocol.MsgHello}})
	s.Handle(Inbound{ID: 2, Token: 22, Payload: []byte{protocol.MsgHello}})
	s.publish([]match.Event{match.PlayerKilled{Killer: 1, Victim: 2}})
	s.Tick(time.Unix(0, 0))
	if ft.events(1)[protocol.MsgKill] != 1 || ft.events(2)[protocol.MsgKill] != 1 {
		t.Fatal("KILL not sent to everyone")
	}
	// Not acked: resent after EventResend.
	s.Tick(time.Unix(0, 0).Add(EventResend))
	if ft.events(2)[protocol.MsgKill] != 2 {
		t.Fatal("KILL not resent")
	}
	s.Remove(2)
	s.Tick(time.Unix(1, 0))
	if ft.events(1)[protocol.MsgPlayerLeft] != 1 {
		t.Fatal("PLAYER_LEFT not sent")
	}
}

func TestSnapshotFitsPacket(t *testing.T) {
	m := match.New(nil, 1)
	s := New(m, &fakeTransport{}, maxPayload)
	var players []match.Player
	for i := 0; i < 16; i++ {
		players = append(players, match.Player{ID: uint8(i)})
	}
	bullets := make([]match.Bullet, 500)
	if n := len(protocol.EncodeSnapshot(s.snapshot(0, players, bullets))); n > maxPayload {
		t.Fatalf("snapshot %d > %d", n, maxPayload)
	}
}
