package lightweight_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	lightweight "github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go"
)

// These are documentation first and tests second, which decides what they may
// do: they render on pkg.go.dev as the first code a prospective consumer reads,
// and they run in CI on every commit.
//
// So none of them makes a network call. An example that dialled a real server
// would be an example that fails in an airgapped CI, fails behind a proxy, and
// teaches the reader that using this package requires a running LIGHTWEIGHT —
// when the interesting part, the part they get wrong, is the shape of the API
// and the shape of the errors. Real network behaviour is proven by
// scripts/sdk-acceptance.sh against an actual stack, which is where that
// evidence belongs.

// The whole configuration surface: three values, and nowhere to put a fourth.
func Example() {
	// In a real backend this is NewClientFromEnv, reading LIGHTWEIGHT_URL,
	// LIGHTWEIGHT_WORKSPACE_ID and LIGHTWEIGHT_API_KEY.
	client, err := lightweight.NewClient(lightweight.Config{
		BaseURL:     "https://identity.example.com",
		WorkspaceID: "ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		APIKey:      "lw_sk_example_credential_value",
	})
	if err != nil {
		fmt.Println("configuration:", err)
		return
	}

	fmt.Println(client.BaseURL())
	fmt.Println(client.WorkspaceID())

	// Output:
	// https://identity.example.com
	// ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301
}

// A Client never prints its credential, including through %v and %+v.
func ExampleClient_String() {
	client, _ := lightweight.NewClient(lightweight.Config{
		BaseURL:     "https://identity.example.com",
		WorkspaceID: "ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		APIKey:      "lw_sk_a_real_secret_that_must_not_be_logged",
	})

	// This is what reaches a log line that interpolates the client.
	fmt.Println(client)

	// Output:
	// lightweight.Client{BaseURL:"https://identity.example.com", WorkspaceID:"ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301", APIKey:lw_sk_a…}
}

// The three error kinds, which exist because a caller reacts to them
// differently: a refusal is a decision, a transport failure may be worth
// retrying, and an unreadable answer is a bug on one side or the other.
func ExampleAPIError() {
	// err would come from any client call; constructed here so the example
	// makes no network request.
	var err error = &lightweight.APIError{
		StatusCode: http.StatusForbidden,
		Code:       lightweight.CodeInsufficientScope,
		Message:    "credential lacks users:read",
		RequestID:  "req_01HZY3",
	}

	var apiErr *lightweight.APIError
	var reqErr *lightweight.RequestError
	switch {
	case errors.As(err, &apiErr):
		// The credential is missing a scope. No retry will fix it; an operator
		// has to mint a new credential.
		fmt.Printf("refused: %s (request %s)\n", apiErr.Code, apiErr.RequestID)
	case errors.As(err, &reqErr):
		fmt.Println("never reached the server:", reqErr)
	default:
		fmt.Println(err)
	}

	// Output:
	// refused: insufficient_scope (request req_01HZY3)
}

// A cancelled or timed-out call unwraps to the standard library's sentinel, so
// existing error handling keeps working.
func ExampleRequestError() {
	var err error = &lightweight.RequestError{
		Op:     "users.list",
		Method: http.MethodGet,
		Path:   "/v1/workspaces/ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301/users",
		Err:    context.DeadlineExceeded,
	}

	fmt.Println(errors.Is(err, context.DeadlineExceeded))

	// Output:
	// true
}

// Paging is explicit: the server reports the page size it actually applied,
// which is not necessarily the one that was asked for.
func ExampleUserListOptions() {
	opts := lightweight.UserListOptions{
		Search: "@example.com",
		First:  0,
		Max:    50,
	}

	fmt.Printf("%s / first=%d / max=%d\n", opts.Search, opts.First, opts.Max)

	// Output:
	// @example.com / first=0 / max=50
}

// The client adds no timeout of its own, so the caller's context is the only
// deadline. This is the shape of every call in the package.
func ExampleUsersService_List() {
	client, err := lightweight.NewClient(lightweight.Config{
		BaseURL:     "https://identity.example.com",
		WorkspaceID: "ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		APIKey:      "lw_sk_example_credential_value",
	})
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Requires the users:read scope on the Project Credential.
	page, err := client.Users.List(ctx, lightweight.UserListOptions{Max: 50})
	if err != nil {
		// Not reached in this example: no request is made against a real server.
		return
	}
	for _, u := range page.Users {
		fmt.Println(u.ID, u.Email)
	}
}
