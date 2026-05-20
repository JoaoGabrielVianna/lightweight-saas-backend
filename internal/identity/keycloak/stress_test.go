// Stress tests for list endpoints that fan out over realm-wide user data:
//
//   - ListInvitations
//   - ListUsersByRole
//
// Scenarios at three scales — 100, 500, 1000 users — exercise the pagination
// loop end-to-end through an httptest stub of Keycloak's first/max-paginated
// endpoints. Pre-pagination, ListInvitations capped at 200 and ListUsersByRole
// inherited Keycloak's default 100; the 500 and 1000 cases reliably failed
// before the fix and pass after.
//
// Timing is captured per scenario (logged via t.Logf) so a regression in the
// pagination loop's per-page overhead surfaces in CI output rather than only
// at production scale.

package keycloak

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// paginatedUserListHandler builds an httptest handler that responds to
// /admin/realms/saas/users (and /roles/<role>/users) honoring Keycloak's
// first/max query parameters. The handler slices `allUsersJSON` at the
// requested window and returns the page.
//
// When max is unset or <= 0, falls back to 100 — Keycloak's documented
// default for /users when no max is supplied. This mirrors the previous
// (pre-pagination) behavior so the "before" tests fail at >100 records on
// ListUsersByRole.
func paginatedUserListHandler(allUsersJSON []string) func(r *http.Request) (int, string) {
	return func(r *http.Request) (int, string) {
		q := r.URL.Query()
		first, _ := strconv.Atoi(q.Get("first"))
		max, _ := strconv.Atoi(q.Get("max"))
		if max <= 0 {
			max = 100
		}
		if first < 0 {
			first = 0
		}
		if first >= len(allUsersJSON) {
			return 200, "[]"
		}
		end := first + max
		if end > len(allUsersJSON) {
			end = len(allUsersJSON)
		}
		return 200, "[" + strings.Join(allUsersJSON[first:end], ",") + "]"
	}
}

// makeInvitationUserJSON returns one user JSON literal that ListInvitations
// will recognize as an invitation (carries a required action).
func makeInvitationUserJSON(i int) string {
	return fmt.Sprintf(`{"id":"u-%d","username":"user%d@x","email":"user%d@x","enabled":true,"requiredActions":["VERIFY_EMAIL"]}`, i, i, i)
}

// makeRoleUserJSON returns one user JSON literal for the ListUsersByRole
// fixture. Plain users — no required actions needed.
func makeRoleUserJSON(i int) string {
	return fmt.Sprintf(`{"id":"u-%d","username":"user%d","email":"user%d@x","enabled":true}`, i, i, i)
}

// stressScales lets every scenario run all three sizes from one declaration.
var stressScales = []int{100, 500, 1000}

// ─────────────── ListInvitations ───────────────

func TestListInvitations_Stress_AllScales(t *testing.T) {
	for _, n := range stressScales {
		n := n
		t.Run(fmt.Sprintf("%dUsers", n), func(t *testing.T) {
			users := make([]string, n)
			for i := 0; i < n; i++ {
				users[i] = makeInvitationUserJSON(i)
			}
			s := newStageAStub(t)
			s.on("GET", "/admin/realms/saas/users", paginatedUserListHandler(users))
			p := newStageAProvider(t, s)

			start := time.Now()
			invs, err := p.ListInvitations(context.Background())
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("ListInvitations: %v", err)
			}
			if len(invs) != n {
				t.Errorf("expected %d invitations, got %d — pagination failed to walk every page", n, len(invs))
			}
			// Spot-check: first and last invitations must both be present and
			// carry the correct identity, proving the iteration covered both
			// ends of the window (not just clipped at page boundaries).
			ids := map[string]bool{}
			for _, inv := range invs {
				ids[inv.ID] = true
			}
			if !ids["u-0"] {
				t.Errorf("first record (u-0) missing from result")
			}
			if !ids[fmt.Sprintf("u-%d", n-1)] {
				t.Errorf("last record (u-%d) missing from result", n-1)
			}
			t.Logf("ListInvitations %4d users → %4d invitations in %s", n, len(invs), elapsed)
		})
	}
}

func TestListInvitations_Stress_FiltersNonInvitationsAtScale(t *testing.T) {
	// Even at 1000 users, the invitation predicate must still apply. Mix
	// half-invitation, half-plain users; verify only the invitations come
	// back. Catches a class of pagination bugs where the loop forgets to
	// re-apply the filter on subsequent pages.
	const total = 1000
	users := make([]string, total)
	for i := 0; i < total; i++ {
		if i%2 == 0 {
			users[i] = makeInvitationUserJSON(i)
		} else {
			// Plain user — no required actions, no invited_by attribute.
			users[i] = fmt.Sprintf(`{"id":"plain-%d","username":"p%d","email":"p%d@x","enabled":true}`, i, i, i)
		}
	}
	s := newStageAStub(t)
	s.on("GET", "/admin/realms/saas/users", paginatedUserListHandler(users))
	p := newStageAProvider(t, s)

	invs, err := p.ListInvitations(context.Background())
	if err != nil {
		t.Fatalf("ListInvitations: %v", err)
	}
	if len(invs) != total/2 {
		t.Errorf("expected %d invitations (half of %d), got %d", total/2, total, len(invs))
	}
	for _, inv := range invs {
		if !strings.HasPrefix(inv.ID, "u-") {
			t.Errorf("non-invitation leaked into result: %+v", inv)
		}
	}
}

// ─────────────── ListUsersByRole ───────────────

func TestListUsersByRole_Stress_AllScales(t *testing.T) {
	for _, n := range stressScales {
		n := n
		t.Run(fmt.Sprintf("%dUsers", n), func(t *testing.T) {
			users := make([]string, n)
			for i := 0; i < n; i++ {
				users[i] = makeRoleUserJSON(i)
			}
			s := newStageAStub(t)
			s.on("GET", "/admin/realms/saas/roles/editor/users", paginatedUserListHandler(users))
			p := newStageAProvider(t, s)

			start := time.Now()
			usersOut, err := p.ListUsersByRole(context.Background(), "editor")
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("ListUsersByRole: %v", err)
			}
			if len(usersOut) != n {
				t.Errorf("expected %d users, got %d — pagination failed to walk every page", n, len(usersOut))
			}
			ids := map[string]bool{}
			for _, u := range usersOut {
				ids[u.ID] = true
			}
			if !ids["u-0"] {
				t.Errorf("first record (u-0) missing")
			}
			if !ids[fmt.Sprintf("u-%d", n-1)] {
				t.Errorf("last record (u-%d) missing", n-1)
			}
			t.Logf("ListUsersByRole %4d users → %4d returned in %s", n, len(usersOut), elapsed)
		})
	}
}

// ─────────────── Safety cap ───────────────

// TestListInvitations_HardCap_PreventsRunaway verifies the pagination loop
// terminates at the safety cap when an upstream stub returns full pages
// indefinitely. Without this cap, a buggy Keycloak (or a stub like this one)
// could pin the request goroutine forever.
func TestListInvitations_HardCap_PreventsRunaway(t *testing.T) {
	s := newStageAStub(t)
	// Always return a full page — pagination would never terminate on its own.
	s.on("GET", "/admin/realms/saas/users", func(r *http.Request) (int, string) {
		first, _ := strconv.Atoi(r.URL.Query().Get("first"))
		// Generate exactly invitationsPageSize entries with unique ids
		// derived from `first`, so the result still merges meaningfully.
		var b strings.Builder
		b.WriteByte('[')
		for i := 0; i < invitationsPageSize; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"id":"u-%d","email":"u%d@x","enabled":true,"requiredActions":["VERIFY_EMAIL"]}`, first+i, first+i)
		}
		b.WriteByte(']')
		return 200, b.String()
	})
	p := newStageAProvider(t, s)

	invs, err := p.ListInvitations(context.Background())
	if err != nil {
		t.Fatalf("ListInvitations: %v", err)
	}
	if len(invs) != invitationsHardCap {
		t.Errorf("expected hard cap to limit result to %d, got %d", invitationsHardCap, len(invs))
	}
}
