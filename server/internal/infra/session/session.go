// Package session is the session table shared by the HTTPS API (creates
// sessions) and the UDP adapter (authenticates packets, seals replies).
package session

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"shooter/server/internal/infra/aead"
)

var ErrFull = errors.New("server full")

// MaxNameLen matches protocol.MaxNameLen.
const MaxNameLen = 16

// Packet is an authenticated, decrypted client payload.
type Packet struct {
	PlayerID uint8
	Token    uint64
	Name     string
	Payload  []byte
}

type Session struct {
	Token    uint64
	PlayerID uint8
	Name     string // immutable after creation

	cipher   *aead.Cipher
	addr     *net.UDPAddr // nil until the first authentic packet
	sendCtr  uint64
	replay   aead.ReplayWindow
	lastSeen time.Time
}

type Table struct {
	mu      sync.Mutex
	byToken map[uint64]*Session
	byID    map[uint8]*Session
	max     int
}

func NewTable(max int) *Table {
	if max > 255 {
		max = 255
	}
	return &Table{byToken: map[uint64]*Session{}, byID: map[uint8]*Session{}, max: max}
}

func SanitizeName(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		if r >= 0x20 && r < 0x7f && b.Len() < MaxNameLen {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "player"
	}
	return b.String()
}

// Create registers a new session and returns its token, key and player id.
func (s *Table) Create(name string, now time.Time) (token uint64, key []byte, id uint8, err error) {
	key = make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return 0, nil, 0, err
	}
	c, err := aead.New(key)
	if err != nil {
		return 0, nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.byID) >= s.max {
		return 0, nil, 0, ErrFull
	}
	for i := 0; i < s.max; i++ {
		if _, used := s.byID[uint8(i)]; !used {
			id = uint8(i)
			break
		}
	}
	for {
		var b [8]byte
		if _, err = rand.Read(b[:]); err != nil {
			return 0, nil, 0, err
		}
		token = binary.LittleEndian.Uint64(b[:])
		if _, dup := s.byToken[token]; token != 0 && !dup {
			break
		}
	}
	sess := &Session{Token: token, PlayerID: id, Name: SanitizeName(name), cipher: c, lastSeen: now}
	s.byToken[token] = sess
	s.byID[id] = sess
	return token, key, id, nil
}

func (s *Table) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byID)
}

func (s *Table) Remove(id uint8) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.byID[id]; ok {
		delete(s.byID, id)
		delete(s.byToken, sess.Token)
	}
}

// Open authenticates a client packet. On success it updates the session's
// address (follows NAT rebinding), replay window and last-seen time.
func (s *Table) Open(pkt []byte, from *net.UDPAddr, now time.Time) (Packet, bool) {
	tok, ok := aead.PeekToken(pkt)
	if !ok {
		return Packet{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.byToken[tok]
	if sess == nil {
		return Packet{}, false
	}
	ctr := aead.PeekCounter(pkt)
	if !sess.replay.Check(ctr) {
		return Packet{}, false
	}
	payload, err := sess.cipher.Open(aead.DirC2S, pkt)
	if err != nil {
		return Packet{}, false
	}
	sess.replay.Mark(ctr)
	a := *from
	sess.addr = &a
	sess.lastSeen = now
	return Packet{PlayerID: sess.PlayerID, Token: tok, Name: sess.Name, Payload: payload}, true
}

// Seal encrypts a payload for player id and returns the packet and address.
func (s *Table) Seal(id uint8, payload []byte) ([]byte, *net.UDPAddr, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.byID[id]
	if sess == nil || sess.addr == nil || sess.sendCtr >= aead.MaxCounter {
		return nil, nil, false
	}
	sess.sendCtr++
	return sess.cipher.Seal(aead.DirS2C, sess.Token, sess.sendCtr, payload), sess.addr, true
}

// Expired removes sessions silent for longer than timeout and returns their ids.
func (s *Table) Expired(now time.Time, timeout time.Duration) []uint8 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []uint8
	for id, sess := range s.byID {
		if now.Sub(sess.lastSeen) > timeout {
			ids = append(ids, id)
			delete(s.byID, id)
			delete(s.byToken, sess.Token)
		}
	}
	return ids
}
