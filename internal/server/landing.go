package server

import (
	"path/filepath"

	"github.com/gin-gonic/gin"
)

// landingAssetDir is the on-disk root of the public landing page. Served
// from disk for symmetry with the playground and admin console — the repo
// root `web/` tree is the single place static assets live, and Go's
// go:embed can't reach paths outside the embedding package.
const landingAssetDir = "web/landing"

// mountLanding wires GET / to the project's public landing page.
//
// The route is unconditional — there is no flag gate. The landing is the
// project's front door: it carries no auth surface, advertises only public
// metadata (version, module list, links to /admin, /swagger, /health, the
// GitHub repo) and must work in every deployment configuration, including
// ones that ship without the admin console or dev playground.
//
// Sibling routes are not touched:
//
//	GET /health          — operational liveness (unchanged)
//	GET /swagger/*any    — API docs (unchanged)
//	GET /me              — auth-gated (unchanged)
//	GET /admin           — gated by AdminConsoleEnabled / DevPlaygroundEnabled
//	GET /dev/auth        — gated by DevPlaygroundEnabled
//
// The landing references /admin, /swagger, and /health as plain links so
// deployments that disable any of those surfaces simply 404 from the
// landing's button — there's no coupling beyond the URL itself.
func mountLanding(r *gin.Engine) {
	r.GET("/", func(c *gin.Context) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		// no-store keeps edits visible on refresh during development;
		// the page is tiny so cache hit-rate is not load-bearing for prod.
		c.Header("Cache-Control", "no-store")
		c.File(filepath.Join(landingAssetDir, "index.html"))
	})
}
