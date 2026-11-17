// Package api is the HTTPS interface (Gin): join, status, health.
package api

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"shooter/server/internal/infra/session"
)

type JoinRequest struct {
	Name string `json:"name"`
}

type JoinResponse struct {
	Token    string `json:"token"` // 16 hex chars (u64)
	Key      string `json:"key"`   // base64, 32 bytes
	PlayerID uint8  `json:"playerId"`
	UDPAddr  string `json:"udpAddr"`
}

// NewRouter serves the HTTPS API. publicHost overrides the host returned in
// udpAddr; when empty the request's Host header is used.
func NewRouter(sessions *session.Table, maxPlayers, tickRate, udpPort int, publicHost string) *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	r.GET("/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"players":    sessions.Count(),
			"maxPlayers": maxPlayers,
			"tickRate":   tickRate,
		})
	})

	r.POST("/join", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
		var req JoinRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		token, key, id, err := sessions.Create(req.Name, time.Now())
		if errors.Is(err, session.ErrFull) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "server full"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		host := publicHost
		if host == "" {
			if h, _, err := net.SplitHostPort(c.Request.Host); err == nil {
				host = h
			} else {
				host = c.Request.Host
			}
		}
		c.JSON(http.StatusOK, JoinResponse{
			Token:    fmt.Sprintf("%016x", token),
			Key:      base64.StdEncoding.EncodeToString(key),
			PlayerID: id,
			UDPAddr:  net.JoinHostPort(host, strconv.Itoa(udpPort)),
		})
	})
	return r
}
