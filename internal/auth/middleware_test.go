package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// withIdentity returns a route handler that stuffs the given identity into
// the gin context before passing control to the next middleware. Used to
// simulate the state RequireAuth would leave behind.
func withIdentity(id *Identity) gin.HandlerFunc {
	return func(c *gin.Context) {
		if id != nil {
			StoreIdentity(c, id)
		}
		c.Next()
	}
}

func newGin(handlers ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/protected", append(handlers, func(c *gin.Context) { c.Status(http.StatusOK) })...)
	return r
}

func TestRequireRole_NoIdentity_Returns401(t *testing.T) {
	r := newGin(RequireRole("admin"))
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with no identity, got %d", w.Code)
	}
}

func TestRequireRole_HasRole_PassesThrough(t *testing.T) {
	r := newGin(
		withIdentity(&Identity{Subject: "u1", Roles: []string{"admin", "user"}}),
		RequireRole("admin"),
	)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with admin role, got %d", w.Code)
	}
}

func TestRequireRole_MissingRole_Returns403(t *testing.T) {
	r := newGin(
		withIdentity(&Identity{Subject: "u1", Roles: []string{"user"}}),
		RequireRole("admin"),
	)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 without admin role, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestRequireRole_EmitsForbiddenEvent(t *testing.T) {
	var observed AuthEvent
	prev := SetEventHook(func(e AuthEvent) { observed = e })
	defer SetEventHook(prev)

	r := newGin(
		withIdentity(&Identity{Subject: "u1", Roles: []string{"user"}}),
		RequireRole("admin"),
	)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	if observed.Kind != EventForbidden {
		t.Errorf("expected EventForbidden, got %q", observed.Kind)
	}
	if observed.Subject != "u1" {
		t.Errorf("event missing subject: %+v", observed)
	}
	if observed.Reason == "" {
		t.Errorf("event missing reason: %+v", observed)
	}
}

func TestRequireAnyRole_PassesWhenAnyMatches(t *testing.T) {
	r := newGin(
		withIdentity(&Identity{Subject: "u1", Roles: []string{"editor"}}),
		RequireAnyRole("admin", "editor"),
	)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 when one of the listed roles present, got %d", w.Code)
	}
}

func TestRequireAnyRole_RejectsWhenNoneMatch(t *testing.T) {
	r := newGin(
		withIdentity(&Identity{Subject: "u1", Roles: []string{"viewer"}}),
		RequireAnyRole("admin", "editor"),
	)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 when no listed role present, got %d", w.Code)
	}
}
