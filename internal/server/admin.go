package server

import (
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/docs"
	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/config"
	"github.com/gin-gonic/gin"
)

// adminAssetDir is the on-disk root of the admin console's static assets.
// Mirrors the playground: served from disk because Go's go:embed can't
// reach paths outside the embedding package and we want to keep web/ at
// the repo root for organization parity with future frontend code.
const adminAssetDir = "web/admin"

// mountAdminConsole wires the IAM admin console UI when
// cfg.DevPlaygroundEnabled is true. The console is currently bundled with
// the playground gate; a future sprint will split it behind its own
// ADMIN_CONSOLE_ENABLED flag once the surface is production-shaped.
//
// Routes:
//
//	GET /admin                  — HTML shell (single page; client-side routing)
//	GET /admin/config.json      — runtime config the SPA fetches on boot
//	GET /admin/static/*filepath — CSS/JS assets
//
// The shell itself is unauthenticated; every action it performs requires the
// caller to log in via the embedded PKCE flow, exactly like /dev/auth.
func mountAdminConsole(r *gin.Engine, cfg *config.Config) {
	if !cfg.DevPlaygroundEnabled {
		return
	}

	log.Warn("DEV_PLAYGROUND_ENABLED=true — mounting /admin console (DEV-ONLY). Do not run this in production yet.")

	r.GET("/admin", func(c *gin.Context) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Header("Cache-Control", "no-store")
		c.File(filepath.Join(adminAssetDir, "index.html"))
	})

	// Runtime config — the SPA needs to know the public Keycloak URL,
	// realm, OIDC client id, and where to land after the PKCE callback.
	// Mirrors /dev/auth/config.json so the two consoles share a contract.
	r.GET("/admin/config.json", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"keycloakUrl": cfg.KeycloakURL,
			"realm":       cfg.KeycloakRealm,
			"clientId":    cfg.DevPlaygroundClientID,
			"apiBase":     "",
			"redirectUri": "http://localhost:" + cfg.Port + "/admin",
		})
	})

	// Static asset serving. Uses gin's catch-all and explicitly maps the
	// common MIME types — we don't trust whatever the OS happens to think
	// .css is on a given machine.
	r.GET("/admin/static/*filepath", func(c *gin.Context) {
		rel := c.Param("filepath")
		// Defense-in-depth against path traversal even though filepath.Join
		// + a check below should already prevent it.
		if strings.Contains(rel, "..") {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		clean := path.Clean("/" + rel) // ensure leading slash + collapse ./
		full := filepath.Join(adminAssetDir, "static", strings.TrimPrefix(clean, "/"))

		switch {
		case strings.HasSuffix(rel, ".css"):
			c.Header("Content-Type", "text/css; charset=utf-8")
		case strings.HasSuffix(rel, ".js"):
			c.Header("Content-Type", "application/javascript; charset=utf-8")
		case strings.HasSuffix(rel, ".svg"):
			c.Header("Content-Type", "image/svg+xml")
		case strings.HasSuffix(rel, ".html"):
			c.Header("Content-Type", "text/html; charset=utf-8")
		}
		c.Header("Cache-Control", "no-store")
		c.File(full)
	})

	// Embedded documentation. Served from docs.MarkdownFS (a //go:embed of
	// the docs/ tree), so the docs travel inside the binary — no filesystem
	// or symlink assumptions at runtime. The in-app docs viewer
	// (web/admin/static/js/views/docs.js) fetches its sources from this
	// route. Kept as a separate handler from /admin/static/*filepath so
	// the static asset semantics (which are filesystem-backed) stay
	// untouched.
	r.GET("/admin/docs/*filepath", func(c *gin.Context) {
		rel := c.Param("filepath")
		// Defense-in-depth against path traversal — embed.FS already
		// resolves relative to the embed root and would refuse "..", but
		// rejecting at the edge keeps the audit-log line single-source.
		if strings.Contains(rel, "..") {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		// Whitelist .md only — everything we expose is markdown. Refusing
		// other extensions means a config typo upstream can't accidentally
		// turn this into a generic file-serving endpoint.
		if !strings.HasSuffix(rel, ".md") {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		clean := path.Clean("/" + rel)
		// embed.FS uses forward-slash paths regardless of OS, so we use
		// path.Join (not filepath.Join) here.
		full := path.Join("docs", strings.TrimPrefix(clean, "/"))
		// embed.FS stores entries under the embedding package's directory
		// as their base name segment — for our docs/markdown.go embeds,
		// the FS root is the docs/ directory and entries are addressed
		// without the "docs/" prefix. Strip it back off.
		full = strings.TrimPrefix(full, "docs/")

		data, err := docs.MarkdownFS.ReadFile(full)
		if err != nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.Header("Content-Type", "text/markdown; charset=utf-8")
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "text/markdown; charset=utf-8", data)
	})
}
