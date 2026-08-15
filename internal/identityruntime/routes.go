package identityruntime

// MountedWorkspaceIdentityRoutes declares the complete workspace-scoped
// identity surface, as `"<METHOD> <path>"` strings using gin's parameter
// syntax.
//
// It is DECLARATION, not registration: internal/server still mounts each route
// explicitly, because a route table you can read top to bottom is worth more
// than one assembled from a loop. What this list buys is a single place for
// two tests in two packages to agree with:
//
//   - internal/identityruntime walks every route to prove each mutation goes
//     through the write guard. A route missing from that walk is never checked.
//   - internal/server asserts the router mounts exactly these paths. A route
//     declared here but never mounted is dead, and one mounted but not declared
//     escapes the write-guard check.
//
// Neither test can pass while the other's view is stale, so the two packages
// cannot drift apart quietly — which is the failure mode a hand-maintained
// duplicate list in each test would have.
func MountedWorkspaceIdentityRoutes() []string {
	const ws = "/v1/workspaces/:workspace_id"
	return []string{
		"GET " + ws + "/users",
		"POST " + ws + "/users",
		"GET " + ws + "/users/:user_id",
		"PATCH " + ws + "/users/:user_id",
		"DELETE " + ws + "/users/:user_id",

		"GET " + ws + "/users/:user_id/roles",
		"POST " + ws + "/users/:user_id/roles",
		"DELETE " + ws + "/users/:user_id/roles/:role_name",

		"POST " + ws + "/users/:user_id/reset-password",
		"PUT " + ws + "/users/:user_id/password",

		"GET " + ws + "/users/:user_id/sessions",
		"DELETE " + ws + "/users/:user_id/sessions",

		"GET " + ws + "/sessions",
		"DELETE " + ws + "/sessions/:session_id",

		"GET " + ws + "/roles",
		"POST " + ws + "/roles",
		"GET " + ws + "/roles/:role_name",
		"PATCH " + ws + "/roles/:role_name",
		"DELETE " + ws + "/roles/:role_name",
		"GET " + ws + "/roles/:role_name/users",

		"GET " + ws + "/invitations",
		"POST " + ws + "/invitations",
		"DELETE " + ws + "/invitations/:invitation_id",
		"POST " + ws + "/invitations/:invitation_id/resend",
	}
}
