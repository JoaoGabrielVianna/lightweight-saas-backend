// Package lightweight is the official Go client for the LIGHTWEIGHT identity
// API.
//
// It is written for one caller: a backend service that has been issued a
// Project Credential and needs to administer identities inside one workspace.
//
// # The whole configuration surface
//
//	LIGHTWEIGHT_URL           where the API is
//	LIGHTWEIGHT_WORKSPACE_ID  which tenant this backend acts on
//	LIGHTWEIGHT_API_KEY       the Project Credential
//
// Three values, and there is deliberately nowhere to put a fourth. A consuming
// backend never learns which identity provider sits behind LIGHTWEIGHT, which
// tenant of it this workspace routes to, or what credential opens it. An
// operator can repoint the workspace at a different provider configuration and
// this client keeps working, unchanged and unrestarted, because it was never
// told what it was pointing at. That indirection is the product, and this
// package is the smallest possible expression of it.
//
// # Getting started
//
//	client, err := lightweight.NewClientFromEnv()
//	if err != nil {
//		return err
//	}
//
//	page, err := client.Users.List(ctx, lightweight.UserListOptions{Max: 50})
//	if err != nil {
//		return err
//	}
//	for _, u := range page.Users {
//		fmt.Println(u.Email)
//	}
//
// # Errors
//
// Three kinds, kept apart because a caller reacts to them differently:
//
//	*APIError       LIGHTWEIGHT answered, and refused. Carries the stable
//	                machine-readable Code, the HTTP status, an optional Field,
//	                and the RequestID to quote in a support request.
//	*RequestError    the request never produced an answer: DNS, TCP, TLS, a
//	                cancelled context, a deadline. Unwraps, so
//	                errors.Is(err, context.DeadlineExceeded) works.
//	*ProtocolError   an answer arrived that this client could not read.
//
//	var apiErr *lightweight.APIError
//	if errors.As(err, &apiErr) && apiErr.Code == lightweight.CodeInsufficientScope {
//		// the credential is missing a scope; an operator must mint a new one
//	}
//
// Code is an open string, never a closed enum: a server newer than this client
// can return a code this package has no constant for, and it will still decode
// and still be readable.
//
// # Scopes
//
// Every method documents the scope its Project Credential must carry. A
// credential missing one is refused with [CodeInsufficientScope] before the
// request reaches any provider. Scopes are chosen by the operator when the
// credential is minted and cannot be widened by this client.
//
// # What this package will not do
//
//   - It never retries. Not even a GET. A client that silently retries a
//     mutation it believes to be idempotent is a client that eventually creates
//     two users, and this API has no idempotency keys to make that safe. Retry
//     policy belongs to the caller, who knows which of their operations can
//     tolerate it.
//   - It never caches authorization. A credential revoked by an operator stops
//     working on the very next call through a live client, because there is
//     nothing between the call and the server to remember that it used to work.
//   - It never logs. It has no logger, no metrics and no hooks. Instrument it by
//     supplying [Config.HTTPClient] with your own http.RoundTripper, which is
//     where tracing, metrics and retries already belong.
//   - It never touches http.DefaultClient or http.DefaultTransport.
//
// # Concurrency
//
// A [Client] is immutable once constructed and is safe for concurrent use by
// any number of goroutines. Construct one per credential and share it.
package lightweight
