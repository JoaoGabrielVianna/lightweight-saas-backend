package authz

import (
	"sort"
	"strings"
)

// The authorization matrix, derived rather than written down.
//
// Slice 13 proved every Project-accessible route is REPRESENTED by the SDK.
// This file exists for the opposite question — is every Project-accessible route
// PREVENTED from being reached without its capability — and the answer has to be
// mechanical for the same reason the registry itself is:
//
//	new route added
//	  → developer forgets the negative test
//	  → CI stays green
//	  → the route ships unproven
//
// Nothing here is a second list. Every value below is computed from the registry
// and from the scope vocabulary, so a route added to registry.go appears in the
// matrix on the next run whether or not anyone remembered it, and a route
// removed disappears. A hand-kept table would drift, and would drift silently,
// which is the exact failure this is meant to catch.

// RouteRequirement is one row: the route, and what it demands.
//
// The route is carried as METHOD and PATH separately rather than as the packed
// "METHOD /path" key, because every consumer — the tests, the console, a
// generated document — immediately splits it again.
type RouteRequirement struct {
	Method string
	Path   string
	Requirement
}

// Key renders the packed form the registry is indexed by.
func (r RouteRequirement) Key() string { return routeKey(r.Method, r.Path) }

// allRoutes returns every classified route as a struct, sorted by key.
func allRoutes() []RouteRequirement {
	out := make([]RouteRequirement, 0, len(registry))
	for key, req := range registry {
		method, path, _ := strings.Cut(key, " ")
		out = append(out, RouteRequirement{Method: method, Path: path, Requirement: req})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

// ProjectReachableRoutes returns every route a project credential may call, with
// the scope each one demands.
//
// This is THE list the negative matrix iterates. It is the same map Authorize
// consults per request, so a route cannot be in one and not the other.
func ProjectReachableRoutes() []RouteRequirement {
	var out []RouteRequirement
	for _, r := range allRoutes() {
		if !r.OperatorOnly {
			out = append(out, r)
		}
	}
	return out
}

// OperatorOnlyRoutes returns every route no scope can reach.
//
// Needed by the negative matrix as much as its complement: "a project credential
// is refused here whatever it holds" is a property that must be proven per
// route, not assumed from the classification that asserts it.
func OperatorOnlyRoutes() []RouteRequirement {
	var out []RouteRequirement
	for _, r := range allRoutes() {
		if r.OperatorOnly {
			out = append(out, r)
		}
	}
	return out
}

// Family is the resource half of a `resource:verb` scope — `users`, `roles`,
// `sessions`, `invitations`, `audit`.
//
// It is a derivation, not a declaration. The vocabulary already encodes the
// grouping in the scope string, and re-declaring it would create a second place
// for `sessions:revoke` to be classified and a second place for that
// classification to be wrong.
type Family string

// Family returns the resource half of the scope. Empty for a malformed scope,
// which IsKnownScope already rejects everywhere it matters.
func (s Scope) Family() Family {
	resource, _, found := strings.Cut(string(s), ":")
	if !found {
		return ""
	}
	return Family(resource)
}

// Verb returns the action half of the scope: `read`, `write` or `revoke`.
func (s Scope) Verb() string {
	_, verb, found := strings.Cut(string(s), ":")
	if !found {
		return ""
	}
	return verb
}

// IsWrite reports whether holding this scope permits changing provider state.
//
// Anything that is not `read` is a write. Stating it that way round is
// deliberate: a scope added with a new verb — `sessions:revoke` was exactly
// that — is treated as a write until someone decides otherwise, which is the
// direction a mistake should fall. The reverse rule ("write and revoke are
// writes") would silently classify the next new verb as a read.
func (s Scope) IsWrite() bool { return s.Verb() != "read" }

// ReadScopeOf returns the read scope of this scope's family, and whether one
// exists.
//
// The negative matrix needs it to build the read-vs-write case: a credential
// holding ONLY `users:read` must be refused `POST /users`. `audit` has no write
// counterpart and every other family has both, so the lookup is over the
// vocabulary rather than over an assumption about naming.
func ReadScopeOf(s Scope) (Scope, bool) {
	want := Scope(string(s.Family()) + ":read")
	if IsKnownScope(want) {
		return want, true
	}
	return "", false
}

// Families returns every capability family in the vocabulary, sorted.
//
// Used to prove the negative evidence covers every family rather than five
// variations on `users`.
func Families() []Family {
	seen := map[Family]struct{}{}
	var out []Family
	for _, s := range AllScopes() {
		f := s.Family()
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// FamilyOfRoute returns the capability family a project-reachable route belongs
// to. Empty for an operator-only route, which belongs to no family because no
// capability reaches it.
func FamilyOfRoute(r RouteRequirement) Family {
	if r.OperatorOnly {
		return ""
	}
	return r.Scope.Family()
}
