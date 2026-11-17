package main

// Composition root: wires infrastructure adapters to the application service
// and runs the single-goroutine game loop.

import (
	"context"
	"net"
	"net/http"
	"time"

	"shooter/server/internal/api"
	"shooter/server/internal/app"
	"shooter/server/internal/domain/match"
	"shooter/server/internal/infra/aead"
	"shooter/server/internal/infra/session"
	"shooter/server/internal/infra/tlscert"
	"shooter/server/internal/infra/udp"
)

const SessionTimeout = 5 * time.Second

type Config struct {
	HTTPAddr   string
	UDPAddr    string
	CertFile   string
	KeyFile    string
	Hosts      []string // SANs for a generated dev cert
	PublicHost string
	MaxPlayers int
}

// Run starts the HTTPS API, the UDP listener and the game loop, and blocks
// until ctx is cancelled. ready (optional) receives the bound addresses.
func Run(ctx context.Context, cfg Config, ready func(httpAddr, udpAddr net.Addr)) error {
	if err := tlscert.Ensure(cfg.CertFile, cfg.KeyFile, cfg.Hosts); err != nil {
		return err
	}
	sessions := session.NewTable(cfg.MaxPlayers)

	ua, err := net.ResolveUDPAddr("udp", cfg.UDPAddr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", ua)
	if err != nil {
		return err
	}
	defer conn.Close()

	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	udpPort := conn.LocalAddr().(*net.UDPAddr).Port
	srv := &http.Server{
		Handler:           api.NewRouter(sessions, cfg.MaxPlayers, match.TickRate, udpPort, cfg.PublicHost),
		ReadHeaderTimeout: 5 * time.Second,
	}
	httpErr := make(chan error, 1)
	go func() { httpErr <- srv.ServeTLS(ln, cfg.CertFile, cfg.KeyFile) }()

	inbox := make(chan app.Inbound, 1024)
	go udp.ReadLoop(conn, sessions, inbox)

	svc := app.New(
		match.New(match.DefaultWalls, uint64(time.Now().UnixNano())),
		udp.Transport{Conn: conn, Sessions: sessions},
		aead.MaxPayload,
	)
	if ready != nil {
		ready(ln.Addr(), conn.LocalAddr())
	}

	ticker := time.NewTicker(time.Second / match.TickRate)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return srv.Shutdown(sctx)
		case err := <-httpErr:
			return err
		case m := <-inbox:
			svc.Handle(m)
		case now := <-ticker.C:
			for _, id := range sessions.Expired(now, SessionTimeout) {
				svc.Remove(id)
			}
			for _, id := range svc.Tick(now) {
				sessions.Remove(id)
				svc.Remove(id)
			}
		}
	}
}
