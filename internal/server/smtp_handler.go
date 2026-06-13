package server

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
	identitykc "github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity/keycloak"
	"github.com/gin-gonic/gin"
)

// SMTPHandler exposes admin REST endpoints for managing the Keycloak realm's
// SMTP settings and for creating users with a temporary password instead of
// the email-invite flow.
type SMTPHandler struct {
	provider *identitykc.Provider
}

// NewSMTPHandler wraps a keycloak provider. Returns nil when provider is nil
// so the router can guard route registration with a simple nil check.
func NewSMTPHandler(p *identitykc.Provider) *SMTPHandler {
	if p == nil {
		return nil
	}
	return &SMTPHandler{provider: p}
}

// GetSMTP returns the realm's current smtpServer block from Keycloak.
// The password field is redacted before sending to the client — it is
// write-only from the SPA's perspective.
//
// GET /admin/settings/smtp
func (h *SMTPHandler) GetSMTP(c *gin.Context) {
	cfg, err := h.provider.GetSMTPConfig(c.Request.Context())
	if err != nil {
		c.JSON(502, gin.H{"error": gin.H{"message": "keycloak unreachable: " + err.Error()}})
		return
	}
	redacted := *cfg
	if redacted.Password != "" {
		redacted.Password = "••••••••"
	}
	c.JSON(200, gin.H{"smtp": redacted})
}

// UpdateSMTP replaces the realm's SMTP block in Keycloak.
// If the client sends the redacted placeholder (••••••••) the handler
// fetches the current stored password and carries it forward, so a Save
// after a GET never accidentally clears the password.
//
// PUT /admin/settings/smtp
func (h *SMTPHandler) UpdateSMTP(c *gin.Context) {
	var body identitykc.SMTPConfig
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": gin.H{"message": "invalid body: " + err.Error()}})
		return
	}
	if body.Password == "••••••••" {
		current, err := h.provider.GetSMTPConfig(c.Request.Context())
		if err != nil {
			c.JSON(502, gin.H{"error": gin.H{"message": "could not fetch current smtp config: " + err.Error()}})
			return
		}
		body.Password = current.Password
	}
	if err := h.provider.UpdateSMTPConfig(c.Request.Context(), body); err != nil {
		c.JSON(502, gin.H{"error": gin.H{"message": "keycloak update failed: " + err.Error()}})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// TestSMTP dials the SMTP host/port, optionally negotiates STARTTLS or
// implicit TLS, optionally authenticates, then immediately QUITs. It does NOT
// send any email — the goal is pure connection + auth validation without
// side-effects. The client may submit an unsaved config so the operator can
// test before committing.
//
// POST /admin/settings/smtp/test
func (h *SMTPHandler) TestSMTP(c *gin.Context) {
	var body identitykc.SMTPConfig
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": gin.H{"message": "invalid body: " + err.Error()}})
		return
	}
	if body.Password == "••••••••" {
		current, err := h.provider.GetSMTPConfig(c.Request.Context())
		if err != nil {
			c.JSON(502, gin.H{"error": gin.H{"message": "could not fetch stored smtp config: " + err.Error()}})
			return
		}
		body.Password = current.Password
	}
	if body.Host == "" {
		c.JSON(400, gin.H{"error": gin.H{"message": "host is required"}})
		return
	}
	port := body.Port
	if port == "" {
		port = "587"
	}
	addr := net.JoinHostPort(body.Host, port)
	if err := dialSMTP(addr, body); err != nil {
		c.JSON(200, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// dialSMTP opens a TCP connection to addr, negotiates TLS according to cfg,
// optionally authenticates, and immediately QUITs. Returns nil on success.
func dialSMTP(addr string, cfg identitykc.SMTPConfig) error {
	const dialTimeout = 8 * time.Second
	host, _, _ := net.SplitHostPort(addr)
	useSSL := strings.EqualFold(cfg.SSL, "true")
	useSTARTTLS := strings.EqualFold(cfg.StartTLS, "true")

	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return fmt.Errorf("tcp connect to %s: %w", addr, err)
	}

	tlsCfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}

	var client *smtp.Client
	if useSSL {
		tlsConn := tls.Client(conn, tlsCfg)
		if err := tlsConn.Handshake(); err != nil {
			conn.Close()
			return fmt.Errorf("tls handshake: %w", err)
		}
		client, err = smtp.NewClient(tlsConn, host)
	} else {
		client, err = smtp.NewClient(conn, host)
	}
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp handshake: %w", err)
	}
	defer client.Quit()

	if useSTARTTLS && !useSSL {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("starttls: %w", err)
			}
		}
	}

	if strings.EqualFold(cfg.Auth, "true") && cfg.User != "" && cfg.Password != "" {
		a := smtp.PlainAuth("", cfg.User, cfg.Password, host)
		if err := client.Auth(a); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	return nil
}

// CreateUserWithPassword provisions a Keycloak user with a temporary password.
// On first login Keycloak enforces UPDATE_PASSWORD — the user must choose a
// new password before accessing the app.
//
// POST /admin/users/password
func (h *SMTPHandler) CreateUserWithPassword(c *gin.Context) {
	var body struct {
		Email             string   `json:"email"`
		FirstName         string   `json:"first_name"`
		LastName          string   `json:"last_name"`
		TemporaryPassword string   `json:"temporary_password"`
		Roles             []string `json:"roles"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": gin.H{"message": "invalid body: " + err.Error()}})
		return
	}
	if body.Email == "" || body.TemporaryPassword == "" {
		c.JSON(400, gin.H{"error": gin.H{"message": "email and temporary_password are required"}})
		return
	}
	if len(body.TemporaryPassword) < 8 {
		c.JSON(400, gin.H{"error": gin.H{"message": "temporary_password must be at least 8 characters"}})
		return
	}

	user, err := h.provider.CreateUserWithPassword(c.Request.Context(), identitykc.CreateUserWithPasswordRequest{
		Email:             body.Email,
		FirstName:         body.FirstName,
		LastName:          body.LastName,
		TemporaryPassword: body.TemporaryPassword,
		Roles:             body.Roles,
	})
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "409") || strings.Contains(strings.ToLower(msg), "conflict") {
			c.JSON(409, gin.H{"error": gin.H{"message": "a user with that email already exists"}})
			return
		}
		c.JSON(502, gin.H{"error": gin.H{"message": "keycloak: " + msg}})
		return
	}

	actor, _ := auth.IdentityFrom(c)
	log.Info("user provisioned with temp-password email=" + user.Email + " by=" + actor.Email)
	c.JSON(201, gin.H{"user": user})
}
