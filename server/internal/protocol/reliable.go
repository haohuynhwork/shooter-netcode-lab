package protocol

// Reliable events over UDP. The server keeps an Outbox per player and resends
// until acked. The client keeps an AckTracker (mirrored in client/net.h) and
// reports "highest seq received + 32 bits of older ones" in every INPUT.

import "time"

// AckWindow is how far behind the latest ack an event can still be acked.
const AckWindow = 32

type pendingEvent struct {
	seq      uint16
	payload  []byte
	lastSent time.Time
	sent     bool
}

type Outbox struct {
	next    uint16
	pending []pendingEvent
}

func NewOutbox() *Outbox { return &Outbox{next: 1} }

// Push queues an event and returns its seq. Seq 0 is never used, so an ack of
// 0 from a client means "nothing received yet".
func (o *Outbox) Push(typ uint8, body []byte) uint16 {
	if o.next == 0 {
		o.next = 1
	}
	seq := o.next
	o.next++
	o.pending = append(o.pending, pendingEvent{seq: seq, payload: EventPayload(typ, seq, body)})
	return seq
}

// Ack drops events the client confirmed, plus any so far behind the ack that
// the client's window can no longer report them.
func (o *Outbox) Ack(ack uint16, bits uint32) {
	if ack == 0 {
		return
	}
	keep := o.pending[:0]
	for _, e := range o.pending {
		d := int16(ack - e.seq)
		switch {
		case d == 0:
		case d >= 1 && d <= AckWindow && bits&(uint32(1)<<(d-1)) != 0:
		case d > AckWindow:
		default:
			keep = append(keep, e)
		}
	}
	o.pending = keep
}

// Due returns events never sent or not sent within interval, marking them sent.
func (o *Outbox) Due(now time.Time, interval time.Duration) [][]byte {
	var out [][]byte
	for i := range o.pending {
		e := &o.pending[i]
		if !e.sent || now.Sub(e.lastSent) >= interval {
			e.sent = true
			e.lastSent = now
			out = append(out, e.payload)
		}
	}
	return out
}

func (o *Outbox) Len() int { return len(o.pending) }

// AckTracker is the receiving side: it dedupes events and produces ack fields.
type AckTracker struct {
	Ack  uint16
	Bits uint32 // bit i => (Ack - 1 - i) received
	any  bool
}

// Receive reports whether seq is new (should be processed).
func (t *AckTracker) Receive(seq uint16) bool {
	if !t.any {
		t.any, t.Ack, t.Bits = true, seq, 0
		return true
	}
	d := int16(seq - t.Ack)
	switch {
	case d == 0:
		return false
	case d > 0:
		if d > AckWindow {
			t.Bits = 0
		} else {
			t.Bits = t.Bits<<d | uint32(1)<<(d-1)
		}
		t.Ack = seq
		return true
	default:
		k := -int(d)
		if k > AckWindow {
			return false
		}
		m := uint32(1) << (k - 1)
		if t.Bits&m != 0 {
			return false
		}
		t.Bits |= m
		return true
	}
}
