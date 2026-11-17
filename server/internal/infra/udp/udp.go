// Package udp adapts the UDP socket to the application: inbound packets are
// authenticated via the session table, outbound payloads are sealed.
package udp

import (
	"errors"
	"net"
	"time"

	"shooter/server/internal/app"
	"shooter/server/internal/infra/aead"
	"shooter/server/internal/infra/session"
)

// Transport implements app.Transport.
type Transport struct {
	Conn     *net.UDPConn
	Sessions *session.Table
}

func (t Transport) Send(id uint8, payload []byte) {
	if len(payload) > aead.MaxPayload {
		return
	}
	pkt, addr, ok := t.Sessions.Seal(id, payload)
	if !ok {
		return
	}
	_, _ = t.Conn.WriteToUDP(pkt, addr)
}

// ReadLoop forwards authentic packets to out until conn is closed. Bad packets
// are dropped silently: no reply, no per-packet logging.
func ReadLoop(conn *net.UDPConn, sessions *session.Table, out chan<- app.Inbound) {
	buf := make([]byte, 2048)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		if n > aead.MaxPacket {
			continue
		}
		p, ok := sessions.Open(buf[:n], addr, time.Now())
		if !ok {
			continue
		}
		select {
		case out <- app.Inbound{ID: p.PlayerID, Token: p.Token, Name: p.Name, Payload: p.Payload}:
		default: // game loop behind; unreliable input can be dropped
		}
	}
}
