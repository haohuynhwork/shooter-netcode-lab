// Package app is the application layer: it drives the Match aggregate from
// network messages and turns domain events into protocol messages. It owns no
// sockets or keys; those arrive through the Transport port.
package app

import (
	"time"

	"shooter/server/internal/domain/match"
	"shooter/server/internal/protocol"
)

const (
	EventResend      = 100 * time.Millisecond
	MaxPendingEvents = 256
)

// Transport is the outbound port, implemented by infra/udp.
type Transport interface {
	Send(id uint8, payload []byte)
}

// Inbound is an authenticated, decrypted message from a player.
type Inbound struct {
	ID      uint8
	Token   uint64 // identifies the session; guards against a reused id
	Name    string
	Payload []byte
}

type peer struct {
	token  uint64
	outbox *protocol.Outbox
}

// Service must be used from a single goroutine.
type Service struct {
	match      *match.Match
	peers      map[uint8]*peer
	out        Transport
	maxPayload int
}

func New(m *match.Match, out Transport, maxPayload int) *Service {
	return &Service{match: m, peers: map[uint8]*peer{}, out: out, maxPayload: maxPayload}
}

func (s *Service) Handle(in Inbound) {
	if len(in.Payload) == 0 {
		return
	}
	p := s.peers[in.ID]
	if p != nil && p.token != in.Token {
		return // stale packet from a previous holder of this id
	}
	switch in.Payload[0] {
	case protocol.MsgHello:
		if p == nil {
			s.join(in)
		}
	case protocol.MsgInput:
		if p == nil {
			return
		}
		msg, err := protocol.DecodeInput(in.Payload)
		if err != nil {
			return
		}
		p.outbox.Ack(msg.EventAck, msg.EventAckBits)
		s.match.QueueInput(in.ID, match.Input{
			Seq: msg.Seq, MoveX: msg.MoveX, MoveY: msg.MoveY, Aim: msg.Aim,
			Shoot: msg.Buttons&protocol.ButtonShoot != 0,
		})
	case protocol.MsgPing:
		if t, err := protocol.DecodePing(in.Payload); err == nil {
			s.out.Send(in.ID, protocol.PingPayload(protocol.MsgPong, t))
		}
	}
}

func (s *Service) join(in Inbound) {
	p := &peer{token: in.Token, outbox: protocol.NewOutbox()}
	p.outbox.Push(protocol.MsgWelcome, protocol.WelcomeBody(in.ID, match.MapW, match.MapH, toWireWalls(s.match.Walls())))
	for _, o := range s.match.Players() {
		p.outbox.Push(protocol.MsgPlayerInfo, protocol.PlayerInfoBody(o.ID, o.Name))
	}
	events, err := s.match.Join(in.ID, in.Name)
	if err != nil {
		return
	}
	s.peers[in.ID] = p
	s.publish(events)
}

// Remove drops a player whose session ended.
func (s *Service) Remove(id uint8) {
	delete(s.peers, id)
	s.publish(s.match.Leave(id))
}

// Tick steps the match, sends snapshots and due events, and returns players to
// kick because they stopped acking reliable events.
func (s *Service) Tick(now time.Time) (kick []uint8) {
	s.publish(s.match.Step())
	players := s.match.Players()
	bullets := s.match.Bullets()
	for _, pl := range players {
		p := s.peers[pl.ID]
		if p == nil {
			continue
		}
		s.out.Send(pl.ID, protocol.EncodeSnapshot(s.snapshot(pl.LastSeq, players, bullets)))
		for _, ev := range p.outbox.Due(now, EventResend) {
			s.out.Send(pl.ID, ev)
		}
		if p.outbox.Len() > MaxPendingEvents {
			kick = append(kick, pl.ID)
		}
	}
	return kick
}

// publish translates domain events into reliable protocol events for everyone.
func (s *Service) publish(events []match.Event) {
	for _, e := range events {
		var typ uint8
		var body []byte
		switch e := e.(type) {
		case match.PlayerJoined:
			typ, body = protocol.MsgPlayerInfo, protocol.PlayerInfoBody(e.ID, e.Name)
		case match.PlayerLeft:
			typ, body = protocol.MsgPlayerLeft, protocol.PlayerLeftBody(e.ID)
		case match.PlayerKilled:
			typ, body = protocol.MsgKill, protocol.KillBody(e.Killer, e.Victim)
		case match.PlayerRespawned:
			typ, body = protocol.MsgRespawn, protocol.RespawnBody(e.ID, e.X, e.Y)
		default:
			continue
		}
		for _, p := range s.peers {
			p.outbox.Push(typ, body)
		}
	}
}

func (s *Service) snapshot(lastSeq uint32, players []match.Player, bullets []match.Bullet) protocol.Snapshot {
	snap := protocol.Snapshot{Tick: s.match.CurrentTick(), LastAckedSeq: lastSeq}
	for _, p := range players {
		snap.Players = append(snap.Players, protocol.PlayerState{
			ID: p.ID, X: p.X, Y: p.Y, Angle: p.Angle, HP: p.HP,
			Alive: p.Alive, Kills: p.Kills, Deaths: p.Deaths,
		})
	}
	room := (s.maxPayload - protocol.SnapshotHeaderSize - len(snap.Players)*protocol.PlayerStateSize) / protocol.BulletStateSize
	room = max(0, min(room, 255))
	for _, b := range bullets {
		if len(snap.Bullets) >= room {
			break
		}
		snap.Bullets = append(snap.Bullets, protocol.BulletState{ID: b.ID, X: b.X, Y: b.Y})
	}
	return snap
}

func toWireWalls(ws []match.Wall) []protocol.Wall {
	out := make([]protocol.Wall, len(ws))
	for i, w := range ws {
		out[i] = protocol.Wall{X: w.X, Y: w.Y, W: w.W, H: w.H}
	}
	return out
}
