package aead

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// Shared with client/tests/core_test.cpp: key = 00..1f, token = 0x0102030405060708, payload "hello".
const (
	vectorC2S = "08070605040302010100000000000000f7329f33daea476d8ac19392d92f14985db32b86d2" // dir 0, ctr 1
	vectorS2C = "08070605040302010700000000000000120b81c85c38db5851fa6f464021decef5a0486e6d" // dir 1, ctr 7
)

func testKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}

func TestCryptoVector(t *testing.T) {
	c, _ := New(testKey())
	if got := hex.EncodeToString(c.Seal(DirC2S, 0x0102030405060708, 1, []byte("hello"))); got != vectorC2S {
		t.Fatalf("c2s vector mismatch: %s", got)
	}
	if got := hex.EncodeToString(c.Seal(DirS2C, 0x0102030405060708, 7, []byte("hello"))); got != vectorS2C {
		t.Fatalf("s2c vector mismatch: %s", got)
	}
}

func TestCryptoRoundTripAndRejects(t *testing.T) {
	c, _ := New(testKey())
	pkt := c.Seal(DirC2S, 42, 5, []byte("payload"))
	if len(pkt) != Overhead+7 {
		t.Fatalf("len %d", len(pkt))
	}
	p, err := c.Open(DirC2S, pkt)
	if err != nil || string(p) != "payload" {
		t.Fatalf("open: %v %q", err, p)
	}
	if _, err := c.Open(DirS2C, pkt); err == nil {
		t.Fatal("wrong direction accepted (reflection)")
	}
	for i := range pkt {
		bad := bytes.Clone(pkt)
		bad[i] ^= 1
		if _, err := c.Open(DirC2S, bad); err == nil {
			t.Fatalf("tampered byte %d accepted", i)
		}
	}
	other := testKey()
	other[0] ^= 0xff
	c2, _ := New(other)
	if _, err := c2.Open(DirC2S, pkt); err == nil {
		t.Fatal("wrong key accepted")
	}
	if _, err := c.Open(DirC2S, c.Seal(DirC2S, 42, 0, nil)); err == nil {
		t.Fatal("counter 0 accepted")
	}
	if _, err := c.Open(DirC2S, pkt[:Overhead-1]); err == nil {
		t.Fatal("short packet accepted")
	}
}

func TestReplayWindow(t *testing.T) {
	var w ReplayWindow
	accept := func(c uint64) bool {
		if !w.Check(c) {
			return false
		}
		w.Mark(c)
		return true
	}
	for _, c := range []uint64{1, 2, 5, 3} {
		if !accept(c) {
			t.Fatalf("fresh %d rejected", c)
		}
	}
	for _, c := range []uint64{0, 1, 2, 3, 5} {
		if accept(c) {
			t.Fatalf("replay %d accepted", c)
		}
	}
	if !accept(4) {
		t.Fatal("out-of-order 4 rejected")
	}
	if !accept(100) {
		t.Fatal("jump rejected")
	}
	if accept(36) { // 100-36 = 64, outside window
		t.Fatal("too-old accepted")
	}
	if !accept(37) {
		t.Fatal("edge of window rejected")
	}
	if accept(37) {
		t.Fatal("replay at edge accepted")
	}
}
