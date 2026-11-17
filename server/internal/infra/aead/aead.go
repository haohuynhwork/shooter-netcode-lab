// Package aead is the encrypted UDP envelope (ChaCha20-Poly1305).
package aead

// Encrypted UDP envelope:
//
//	u64 token | u64 counter | ciphertext(payload) | 16-byte Poly1305 tag
//
// ChaCha20-Poly1305 (IETF) with the per-session key from /join.
// Nonce (12 bytes) = u32 direction | u64 counter, little-endian.
// The 16-byte header (token, counter) is the associated data.

import (
	"crypto/cipher"
	"encoding/binary"
	"errors"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	DirC2S uint32 = 0
	DirS2C uint32 = 1

	HeaderSize = 16
	TagSize    = 16
	Overhead   = HeaderSize + TagSize
	MaxPacket  = 1200
	MaxPayload = MaxPacket - Overhead

	// Refuse to send long before the 64-bit counter could wrap.
	MaxCounter = uint64(1) << 62
)

var ErrBadPacket = errors.New("crypto: bad packet")

type Cipher struct{ aead cipher.AEAD }

func New(key []byte) (*Cipher, error) {
	a, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: a}, nil
}

func makeNonce(dir uint32, ctr uint64) [12]byte {
	var n [12]byte
	binary.LittleEndian.PutUint32(n[0:4], dir)
	binary.LittleEndian.PutUint64(n[4:12], ctr)
	return n
}

// Seal builds a full wire packet.
func (c *Cipher) Seal(dir uint32, token, ctr uint64, payload []byte) []byte {
	out := make([]byte, HeaderSize, HeaderSize+len(payload)+TagSize)
	binary.LittleEndian.PutUint64(out[0:8], token)
	binary.LittleEndian.PutUint64(out[8:16], ctr)
	n := makeNonce(dir, ctr)
	ad := append([]byte(nil), out[:HeaderSize]...)
	return c.aead.Seal(out, n[:], payload, ad)
}

// Open authenticates and decrypts a wire packet. It does not check replays;
// callers use ReplayWindow with PeekCounter before and after.
func (c *Cipher) Open(dir uint32, pkt []byte) ([]byte, error) {
	if len(pkt) < Overhead || len(pkt) > MaxPacket {
		return nil, ErrBadPacket
	}
	ctr := binary.LittleEndian.Uint64(pkt[8:16])
	if ctr == 0 {
		return nil, ErrBadPacket
	}
	n := makeNonce(dir, ctr)
	p, err := c.aead.Open(nil, n[:], pkt[HeaderSize:], pkt[:HeaderSize])
	if err != nil {
		return nil, ErrBadPacket
	}
	return p, nil
}

func PeekToken(pkt []byte) (uint64, bool) {
	if len(pkt) < Overhead {
		return 0, false
	}
	return binary.LittleEndian.Uint64(pkt[0:8]), true
}

func PeekCounter(pkt []byte) uint64 {
	if len(pkt) < HeaderSize {
		return 0
	}
	return binary.LittleEndian.Uint64(pkt[8:16])
}

// ReplayWindow accepts each counter at most once, and nothing more than 63
// behind the highest seen. Bit i of bits means (max - i) was seen.
type ReplayWindow struct {
	max  uint64
	bits uint64
}

func (w *ReplayWindow) Check(c uint64) bool {
	if c == 0 {
		return false
	}
	if c > w.max {
		return true
	}
	d := w.max - c
	if d >= 64 {
		return false
	}
	return w.bits&(uint64(1)<<d) == 0
}

func (w *ReplayWindow) Mark(c uint64) {
	if c > w.max {
		s := c - w.max
		if s >= 64 {
			w.bits = 1
		} else {
			w.bits = w.bits<<s | 1
		}
		w.max = c
		return
	}
	w.bits |= uint64(1) << (w.max - c)
}
