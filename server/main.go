package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gin-gonic/gin"
)

func main() {
	var cfg Config
	var hosts string
	flag.StringVar(&cfg.HTTPAddr, "http", ":8443", "HTTPS API listen address")
	flag.StringVar(&cfg.UDPAddr, "udp", ":7777", "UDP game listen address")
	flag.StringVar(&cfg.CertFile, "cert", "certs/server.crt", "TLS certificate (generated if missing)")
	flag.StringVar(&cfg.KeyFile, "key", "certs/server.key", "TLS key (generated if missing)")
	flag.StringVar(&hosts, "hosts", "localhost,127.0.0.1", "comma-separated names/IPs for a generated cert")
	flag.StringVar(&cfg.PublicHost, "public-host", "", "host returned in udpAddr (default: request Host)")
	flag.IntVar(&cfg.MaxPlayers, "max-players", 16, "maximum concurrent players")
	flag.Parse()
	cfg.Hosts = strings.Split(hosts, ",")

	gin.SetMode(gin.ReleaseMode)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := Run(ctx, cfg, func(h, u net.Addr) {
		log.Printf("HTTPS API on %s, UDP game on %s, cert %s", h, u, cfg.CertFile)
	})
	if err != nil {
		log.Fatal(err)
	}
}
