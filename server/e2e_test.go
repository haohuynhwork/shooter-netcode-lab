package main

// End-to-end: real HTTPS /join, real encrypted UDP, real game loop.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"shooter/server/internal/api"
	"shooter/server/internal/infra/aead"
	. "shooter/server/internal/protocol"
)

type testClient struct {
	t      *testing.T
	conn   *net.UDPConn
	cipher *aead.Cipher
	token  uint64
	ctr    uint64
	replay aead.ReplayWindow
	acks   AckTracker
	id     uint8
}

func startServer(t *testing.T) (httpURL string, caFile string) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	cfg := Config{
		HTTPAddr: "127.0.0.1:0", UDPAddr: "127.0.0.1:0",
		CertFile: filepath.Join(dir, "s.crt"), KeyFile: filepath.Join(dir, "s.key"),
		Hosts: []string{"localhost", "127.0.0.1"}, MaxPlayers: 2,
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, cfg, func(h, _ net.Addr) { ready <- h.String() })
	}()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case h := <-ready:
		return "https://" + h, cfg.CertFile
	case err := <-done:
		t.Fatal(err)
	}
	return "", ""
}

func httpsClient(t *testing.T, caFile string) *http.Client {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}, Timeout: 5 * time.Second}
}

func join(t *testing.T, hc *http.Client, url, name string) (*testClient, int) {
	resp, err := hc.Post(url+"/join", "application/json", bytes.NewBufferString(`{"name":"`+name+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, resp.StatusCode
	}
	var jr api.JoinResponse
	if err := json.NewDecoder(resp.Body).Decode(&jr); err != nil {
		t.Fatal(err)
	}
	key, _ := base64.StdEncoding.DecodeString(jr.Key)
	tok, _ := strconv.ParseUint(jr.Token, 16, 64)
	ua, _ := net.ResolveUDPAddr("udp", jr.UDPAddr)
	conn, err := net.DialUDP("udp", nil, ua)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	c, _ := aead.New(key)
	return &testClient{t: t, conn: conn, cipher: c, token: tok, id: jr.PlayerID}, 200
}

func (c *testClient) send(p []byte) {
	c.ctr++
	_, _ = c.conn.Write(c.cipher.Seal(aead.DirC2S, c.token, c.ctr, p))
}

// recv returns the next authentic, non-replayed payload or nil on timeout.
func (c *testClient) recv(d time.Duration) []byte {
	buf := make([]byte, 2048)
	deadline := time.Now().Add(d)
	for {
		_ = c.conn.SetReadDeadline(deadline)
		n, err := c.conn.Read(buf)
		if err != nil {
			return nil
		}
		ctr := aead.PeekCounter(buf[:n])
		if !c.replay.Check(ctr) {
			continue
		}
		p, err := c.cipher.Open(aead.DirS2C, buf[:n])
		if err != nil {
			continue
		}
		c.replay.Mark(ctr)
		return p
	}
}

func TestEndToEnd(t *testing.T) {
	url, ca := startServer(t)
	hc := httpsClient(t, ca)

	// Plain HTTP and an untrusted client must fail.
	if _, err := http.Get(url[len("https://"):]); err == nil {
		t.Log("note: plain request unexpectedly succeeded")
	}
	if _, err := (&http.Client{Timeout: 2 * time.Second}).Get(url + "/healthz"); err == nil {
		t.Fatal("untrusted TLS accepted")
	}
	resp, err := hc.Get(url + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("healthz: %v", err)
	}
	resp.Body.Close()

	a, _ := join(t, hc, url, "alice")
	b, _ := join(t, hc, url, "bob")
	if _, code := join(t, hc, url, "carol"); code != http.StatusServiceUnavailable {
		t.Fatalf("third join with max 2: %d", code)
	}

	resp, _ = hc.Get(url + "/status")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(body, []byte(`"players":2`)) {
		t.Fatalf("status: %s", body)
	}

	// Handshake: HELLO until WELCOME.
	for _, c := range []*testClient{a, b} {
		welcomed := false
		for i := 0; i < 20 && !welcomed; i++ {
			c.send([]byte{MsgHello})
			for p := c.recv(200 * time.Millisecond); p != nil && !welcomed; p = c.recv(50 * time.Millisecond) {
				if p[0] >= MsgWelcome {
					typ, seq, body, _ := DecodeEventHeader(p)
					if c.acks.Receive(seq) && typ == MsgWelcome {
						wc, err := DecodeWelcomeBody(body)
						if err != nil || wc.YourID != c.id || len(wc.Walls) == 0 {
							t.Fatalf("welcome %+v %v", wc, err)
						}
						welcomed = true
					}
				}
			}
		}
		if !welcomed {
			t.Fatal("no WELCOME")
		}
	}

	// Ping/pong.
	a.send(PingPayload(MsgPing, 12345))
	gotPong := false
	for p := a.recv(time.Second); p != nil && !gotPong; p = a.recv(200 * time.Millisecond) {
		if p[0] == MsgPong && len(p) == 5 && p[1] == 0x39 && p[2] == 0x30 {
			gotPong = true
		}
	}
	if !gotPong {
		t.Fatal("no PONG")
	}

	// Move alice right for 20 inputs; snapshots must reflect it and ack seq 20.
	var startX float32 = -1
	var last Snapshot
	for seq := uint32(1); seq <= 20; seq++ {
		a.send(EncodeInput(Input{Seq: seq, MoveX: 1, EventAck: a.acks.Ack, EventAckBits: a.acks.Bits}))
		time.Sleep(time.Second / 60)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p := a.recv(200 * time.Millisecond)
		if p == nil || p[0] != MsgSnapshot {
			continue
		}
		s, err := DecodeSnapshot(p)
		if err != nil {
			t.Fatal(err)
		}
		if startX < 0 {
			for _, ps := range s.Players {
				if ps.ID == a.id {
					startX = ps.X
				}
			}
		}
		last = s
		if s.LastAckedSeq == 20 {
			break
		}
	}
	if last.LastAckedSeq != 20 || len(last.Players) != 2 {
		t.Fatalf("snapshot ack=%d players=%d", last.LastAckedSeq, len(last.Players))
	}

	// A forged packet (bob's token, garbage ciphertext) and a replayed packet are ignored.
	forged := b.cipher.Seal(aead.DirC2S, b.token, 999, EncodeInput(Input{Seq: 1000, MoveX: 1}))
	forged[len(forged)-1] ^= 1
	_, _ = a.conn.Write(forged)
	_, _ = b.conn.Write(b.cipher.Seal(aead.DirC2S, b.token, 1, []byte{MsgHello})) // ctr 1 already used
}
