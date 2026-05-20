package identity

import (
	"net/http"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/auth"
)

// recordingInvalidator captures every Invalidate/InvalidateAll call so tests
// can assert that the GAP-1 cache-invalidation hooks fire from the right
// handlers (see docs/SECURITY_REMEDIATION_GAP1.md).
type recordingInvalidator struct {
	mu       sync.Mutex
	subjects []string
	allCount int
}

func (r *recordingInvalidator) Invalidate(subject string) {
	r.mu.Lock()
	r.subjects = append(r.subjects, subject)
	r.mu.Unlock()
}

func (r *recordingInvalidator) InvalidateAll() {
	r.mu.Lock()
	r.allCount++
	r.mu.Unlock()
}

func (r *recordingInvalidator) snapshot() ([]string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]string(nil), r.subjects...)
	return out, r.allCount
}

// satisfies the compile-time interface assertion — also catches drift if
// auth.AdminInvalidator gains methods later.
var _ auth.AdminInvalidator = (*recordingInvalidator)(nil)

func TestSetAdminInvalidator_NilFallsBackToNoop(t *testing.T) {
	h, _ := buildHandler()
	// Pre-condition: NewHandler installs the noop. SetAdminInvalidator(nil)
	// must keep the handler safe rather than panic on later mutations.
	h.SetAdminInvalidator(nil)

	// Calling a mutation handler must not panic. Use a path that emits an
	// invalidation: UnassignRoleFromUser (the GAP-1 hot path).
	w, c := newRequest(t, "DELETE", "/admin/users/"+targetSubject+"/roles/editor", nil,
		gin.Params{{Key: "id", Value: targetSubject}, {Key: "name", Value: "editor"}})
	h.UnassignRoleFromUser(c)

	if c.Writer.Status() != http.StatusNoContent {
		t.Fatalf("status = %d, body=%s", c.Writer.Status(), w.Body.String())
	}
}

func TestUnassignRoleFromUser_InvalidatesTarget(t *testing.T) {
	h, _ := buildHandler()
	inv := &recordingInvalidator{}
	h.SetAdminInvalidator(inv)

	w, c := newRequest(t, "DELETE", "/admin/users/"+targetSubject+"/roles/editor", nil,
		gin.Params{{Key: "id", Value: targetSubject}, {Key: "name", Value: "editor"}})
	h.UnassignRoleFromUser(c)

	if c.Writer.Status() != http.StatusNoContent {
		t.Fatalf("status = %d, body=%s", c.Writer.Status(), w.Body.String())
	}
	subs, _ := inv.snapshot()
	if len(subs) != 1 || subs[0] != targetSubject {
		t.Errorf("expected one Invalidate(%q), got %v", targetSubject, subs)
	}
}

func TestAssignRolesToUser_InvalidatesTarget(t *testing.T) {
	h, _ := buildHandler()
	inv := &recordingInvalidator{}
	h.SetAdminInvalidator(inv)

	w, c := newRequest(t, "POST", "/admin/users/"+targetSubject+"/roles",
		AssignRolesRequestBody{Roles: []string{"admin"}},
		gin.Params{{Key: "id", Value: targetSubject}})
	h.AssignRolesToUser(c)

	if c.Writer.Status() != http.StatusNoContent {
		t.Fatalf("status = %d, body=%s", c.Writer.Status(), w.Body.String())
	}
	subs, _ := inv.snapshot()
	if len(subs) != 1 || subs[0] != targetSubject {
		t.Errorf("expected Invalidate(%q), got %v", targetSubject, subs)
	}
}

func TestUpdateUser_InvalidatesTarget(t *testing.T) {
	h, _ := buildHandler()
	inv := &recordingInvalidator{}
	h.SetAdminInvalidator(inv)

	enabled := true
	w, c := newRequest(t, "PATCH", "/admin/users/"+targetSubject,
		UpdateUserRequestBody{Enabled: &enabled},
		gin.Params{{Key: "id", Value: targetSubject}})
	h.UpdateUser(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	subs, _ := inv.snapshot()
	if len(subs) != 1 || subs[0] != targetSubject {
		t.Errorf("expected Invalidate(%q), got %v", targetSubject, subs)
	}
}

func TestDeleteUser_InvalidatesTarget(t *testing.T) {
	h, _ := buildHandler()
	inv := &recordingInvalidator{}
	h.SetAdminInvalidator(inv)

	w, c := newRequest(t, "DELETE", "/admin/users/"+targetSubject, nil,
		gin.Params{{Key: "id", Value: targetSubject}})
	h.DeleteUser(c)

	if c.Writer.Status() != http.StatusNoContent {
		t.Fatalf("status = %d, body=%s", c.Writer.Status(), w.Body.String())
	}
	subs, _ := inv.snapshot()
	if len(subs) != 1 || subs[0] != targetSubject {
		t.Errorf("expected Invalidate(%q), got %v", targetSubject, subs)
	}
}

func TestDeleteRole_InvalidatesAll(t *testing.T) {
	h, _ := buildHandler()
	inv := &recordingInvalidator{}
	h.SetAdminInvalidator(inv)

	w, c := newRequest(t, "DELETE", "/admin/roles/support", nil,
		gin.Params{{Key: "name", Value: "support"}})
	h.DeleteRole(c)

	if c.Writer.Status() != http.StatusNoContent {
		t.Fatalf("status = %d, body=%s", c.Writer.Status(), w.Body.String())
	}
	_, allCount := inv.snapshot()
	if allCount != 1 {
		t.Errorf("expected 1 InvalidateAll call after role delete, got %d", allCount)
	}
}

// Failure paths must NOT invalidate — invalidating on failure could mask
// retry/recovery semantics and uselessly hammer Keycloak for unchanged data.
func TestUnassignRoleFromUser_FailurePath_DoesNotInvalidate(t *testing.T) {
	h, fp := buildHandler()
	inv := &recordingInvalidator{}
	h.SetAdminInvalidator(inv)
	fp.mutationCalls.unassignErr = ErrAdminAPIUnavailable

	w, c := newRequest(t, "DELETE", "/admin/users/"+targetSubject+"/roles/editor", nil,
		gin.Params{{Key: "id", Value: targetSubject}, {Key: "name", Value: "editor"}})
	h.UnassignRoleFromUser(c)

	if w.Code == http.StatusNoContent {
		t.Fatalf("expected failure status, got 204")
	}
	subs, allCount := inv.snapshot()
	if len(subs) != 0 || allCount != 0 {
		t.Errorf("expected no invalidation on failure, got subs=%v allCount=%d", subs, allCount)
	}
}
