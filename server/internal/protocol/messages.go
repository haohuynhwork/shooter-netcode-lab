// Package protocol is the wire format (little-endian binary) and the
// reliable-event mechanism layered on unreliable datagrams.
package protocol

// Binary protocol (little-endian). These are the plaintext payloads; every
// UDP packet wraps one of them in the encrypted envelope from package aead.

import (
	"encoding/binary"
	"errors"
	"math"
)

const (
	MsgHello      uint8 = 0
	MsgInput      uint8 = 1
	MsgSnapshot   uint8 = 2
	MsgPing       uint8 = 3
	MsgPong       uint8 = 4
	MsgWelcome    uint8 = 10
	MsgKill       uint8 = 11
	MsgRespawn    uint8 = 12
	MsgPlayerInfo uint8 = 13
	MsgPlayerLeft uint8 = 14
)

const ButtonShoot uint8 = 1

const (
	PlayerStateSize    = 19 // id u8, x/y/angle f32, hp u8, alive u8, kills/deaths u16
	BulletStateSize    = 10 // id u16, x/y f32
	SnapshotHeaderSize = 11 // type u8, tick u32, lastAckedSeq u32, nPlayers u8, nBullets u8
	MaxNameLen         = 16
)

var (
	ErrShort   = errors.New("proto: packet too short")
	ErrBadType = errors.New("proto: unexpected message type")
)

type writer struct{ b []byte }

func (w *writer) u8(v uint8)    { w.b = append(w.b, v) }
func (w *writer) i8(v int8)     { w.b = append(w.b, uint8(v)) }
func (w *writer) u16(v uint16)  { w.b = binary.LittleEndian.AppendUint16(w.b, v) }
func (w *writer) u32(v uint32)  { w.b = binary.LittleEndian.AppendUint32(w.b, v) }
func (w *writer) f32(v float32) { w.u32(math.Float32bits(v)) }

type reader struct {
	b   []byte
	bad bool
}

func (r *reader) take(n int) []byte {
	if r.bad || len(r.b) < n {
		r.bad = true
		return make([]byte, n)
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}
func (r *reader) u8() uint8    { return r.take(1)[0] }
func (r *reader) i8() int8     { return int8(r.u8()) }
func (r *reader) u16() uint16  { return binary.LittleEndian.Uint16(r.take(2)) }
func (r *reader) u32() uint32  { return binary.LittleEndian.Uint32(r.take(4)) }
func (r *reader) f32() float32 { return math.Float32frombits(r.u32()) }
func (r *reader) err() error {
	if r.bad {
		return ErrShort
	}
	return nil
}

// ---- Client -> Server ----

type Input struct {
	Seq          uint32
	MoveX, MoveY int8
	Aim          float32
	Buttons      uint8
	EventAck     uint16
	EventAckBits uint32
}

func EncodeInput(in Input) []byte {
	w := writer{b: make([]byte, 0, 18)}
	w.u8(MsgInput)
	w.u32(in.Seq)
	w.i8(in.MoveX)
	w.i8(in.MoveY)
	w.f32(in.Aim)
	w.u8(in.Buttons)
	w.u16(in.EventAck)
	w.u32(in.EventAckBits)
	return w.b
}

func DecodeInput(p []byte) (Input, error) {
	r := reader{b: p}
	if r.u8() != MsgInput {
		return Input{}, ErrBadType
	}
	in := Input{
		Seq: r.u32(), MoveX: r.i8(), MoveY: r.i8(), Aim: r.f32(),
		Buttons: r.u8(), EventAck: r.u16(), EventAckBits: r.u32(),
	}
	return in, r.err()
}

// PingPayload builds a PING (client) or PONG (server echo) message.
func PingPayload(typ uint8, t uint32) []byte {
	w := writer{}
	w.u8(typ)
	w.u32(t)
	return w.b
}

// ---- Server -> Client, unreliable ----

type PlayerState struct {
	ID            uint8
	X, Y, Angle   float32
	HP            uint8
	Alive         bool
	Kills, Deaths uint16
}

type BulletState struct {
	ID   uint16
	X, Y float32
}

type Snapshot struct {
	Tick, LastAckedSeq uint32
	Players            []PlayerState
	Bullets            []BulletState
}

func EncodeSnapshot(s Snapshot) []byte {
	w := writer{b: make([]byte, 0, SnapshotHeaderSize+len(s.Players)*PlayerStateSize+len(s.Bullets)*BulletStateSize)}
	w.u8(MsgSnapshot)
	w.u32(s.Tick)
	w.u32(s.LastAckedSeq)
	w.u8(uint8(len(s.Players)))
	for _, p := range s.Players {
		w.u8(p.ID)
		w.f32(p.X)
		w.f32(p.Y)
		w.f32(p.Angle)
		w.u8(p.HP)
		alive := uint8(0)
		if p.Alive {
			alive = 1
		}
		w.u8(alive)
		w.u16(p.Kills)
		w.u16(p.Deaths)
	}
	w.u8(uint8(len(s.Bullets)))
	for _, b := range s.Bullets {
		w.u16(b.ID)
		w.f32(b.X)
		w.f32(b.Y)
	}
	return w.b
}

func DecodeSnapshot(p []byte) (Snapshot, error) {
	r := reader{b: p}
	if r.u8() != MsgSnapshot {
		return Snapshot{}, ErrBadType
	}
	s := Snapshot{Tick: r.u32(), LastAckedSeq: r.u32()}
	n := int(r.u8())
	for i := 0; i < n && !r.bad; i++ {
		s.Players = append(s.Players, PlayerState{
			ID: r.u8(), X: r.f32(), Y: r.f32(), Angle: r.f32(), HP: r.u8(),
			Alive: r.u8() != 0, Kills: r.u16(), Deaths: r.u16(),
		})
	}
	n = int(r.u8())
	for i := 0; i < n && !r.bad; i++ {
		s.Bullets = append(s.Bullets, BulletState{ID: r.u16(), X: r.f32(), Y: r.f32()})
	}
	return s, r.err()
}

// ---- Server -> Client, reliable events ----
// Wire form: u8 type | u16 eventSeq | body. The Outbox assigns eventSeq.

func EventPayload(typ uint8, seq uint16, body []byte) []byte {
	w := writer{b: make([]byte, 0, 3+len(body))}
	w.u8(typ)
	w.u16(seq)
	w.b = append(w.b, body...)
	return w.b
}

func DecodeEventHeader(p []byte) (typ uint8, seq uint16, body []byte, err error) {
	if len(p) < 3 {
		return 0, 0, nil, ErrShort
	}
	return p[0], binary.LittleEndian.Uint16(p[1:3]), p[3:], nil
}

type Wall struct{ X, Y, W, H uint16 }

type Welcome struct {
	YourID     uint8
	MapW, MapH uint16
	Walls      []Wall
}

func WelcomeBody(id uint8, mapW, mapH uint16, walls []Wall) []byte {
	w := writer{}
	w.u8(id)
	w.u16(mapW)
	w.u16(mapH)
	w.u8(uint8(len(walls)))
	for _, wl := range walls {
		w.u16(wl.X)
		w.u16(wl.Y)
		w.u16(wl.W)
		w.u16(wl.H)
	}
	return w.b
}

func DecodeWelcomeBody(b []byte) (Welcome, error) {
	r := reader{b: b}
	wc := Welcome{YourID: r.u8(), MapW: r.u16(), MapH: r.u16()}
	n := int(r.u8())
	for i := 0; i < n && !r.bad; i++ {
		wc.Walls = append(wc.Walls, Wall{r.u16(), r.u16(), r.u16(), r.u16()})
	}
	return wc, r.err()
}

func KillBody(killer, victim uint8) []byte { return []byte{killer, victim} }

func RespawnBody(id uint8, x, y float32) []byte {
	w := writer{}
	w.u8(id)
	w.f32(x)
	w.f32(y)
	return w.b
}

func PlayerInfoBody(id uint8, name string) []byte {
	if len(name) > MaxNameLen {
		name = name[:MaxNameLen]
	}
	w := writer{}
	w.u8(id)
	w.u8(uint8(len(name)))
	w.b = append(w.b, name...)
	return w.b
}

func PlayerLeftBody(id uint8) []byte { return []byte{id} }

func DecodePing(p []byte) (uint32, error) {
	r := reader{b: p}
	if t := r.u8(); t != MsgPing && t != MsgPong {
		return 0, ErrBadType
	}
	v := r.u32()
	if len(r.b) != 0 {
		return 0, ErrShort
	}
	return v, r.err()
}
