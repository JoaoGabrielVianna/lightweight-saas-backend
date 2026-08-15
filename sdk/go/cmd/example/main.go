// Command example is the smallest useful LIGHTWEIGHT integration.
//
// It is deliberately the ONLY example in this repository. Ten examples are ten
// things to keep working and nine a developer skims past; the first five minutes
// with a client library decide whether there is a sixth, and they are spent on
// exactly this: configure it, make a call, read an error.
//
// Run it with the three variables a Project Credential integration needs:
//
//	LIGHTWEIGHT_URL=https://identity.example.com \
//	LIGHTWEIGHT_WORKSPACE_ID=ws_3f2504e0-4f89-41d3-9a0c-0305e82c3301 \
//	LIGHTWEIGHT_API_KEY=lw_sk_… \
//	go run ./cmd/example
//
// The equivalent curl, for telling an SDK problem apart from a configuration
// one:
//
//	curl -sS -H "Authorization: Bearer $LIGHTWEIGHT_API_KEY" \
//	  "$LIGHTWEIGHT_URL/v1/workspaces/$LIGHTWEIGHT_WORKSPACE_ID/users?max=10"
//
// If curl works and this does not, the bug is here. If neither works, the
// credential or the workspace is wrong, and the error body will say which.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	lightweight "github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	// Three environment variables, and there is nowhere to put a fourth. No
	// provider URL, no tenant name, no client secret: the credential already
	// tells the server everything it needs.
	client, err := lightweight.NewClientFromEnv()
	if err != nil {
		return err
	}

	// A context bounds the call, and Ctrl-C cancels it. The SDK adds no timeout
	// of its own on top of a deadline the caller set.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	page, err := client.Users.List(ctx, lightweight.UserListOptions{Max: 10})
	if err != nil {
		return describe(err)
	}

	fmt.Printf("workspace %s: %d user(s) on this page\n", client.WorkspaceID(), page.Count)
	for _, user := range page.Users {
		status := "enabled"
		if !user.Enabled {
			status = "disabled"
		}
		fmt.Printf("  %-40s %-10s created %s\n",
			user.Email, status, user.CreatedAt.Format(time.RFC3339))
	}
	return nil
}

// describe turns an SDK error into something worth putting in front of a person.
//
// This is the part of the example that matters most. Anyone can call List; what
// a developer needs to have seen once is that failures are TYPED — that
// "the credential is missing a scope" and "the network is down" are different
// values with different reactions, and that neither requires parsing a string.
func describe(err error) error {
	var apiErr *lightweight.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case lightweight.CodeCredentialInvalid:
			return fmt.Errorf("the API key was refused — it may have been revoked or have expired "+
				"(request %s)", apiErr.RequestID)
		case lightweight.CodeInsufficientScope:
			return fmt.Errorf("this credential does not carry users:read; ask your operator for a "+
				"key with that scope (request %s)", apiErr.RequestID)
		case lightweight.CodeWorkspaceMismatch:
			return fmt.Errorf("%s and %s came from different environments (request %s)",
				lightweight.EnvWorkspaceID, lightweight.EnvAPIKey, apiErr.RequestID)
		case lightweight.CodeRateLimitExceeded:
			if wait, ok := apiErr.RetryAfter(); ok {
				return fmt.Errorf("rate limited; retry in %s (request %s)", wait, apiErr.RequestID)
			}
			return fmt.Errorf("rate limited (request %s)", apiErr.RequestID)
		default:
			// Including codes newer than this program. They decode, they are
			// readable, and the request id is what a support conversation needs.
			return fmt.Errorf("%s: %s (request %s)", apiErr.Code, apiErr.Message, apiErr.RequestID)
		}
	}

	// Not a refusal. Either nothing answered, or something answered that was not
	// LIGHTWEIGHT — a distinction worth keeping, because a mutation that failed
	// this way may still have been applied.
	var reqErr *lightweight.RequestError
	if errors.As(err, &reqErr) {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("timed out reaching LIGHTWEIGHT: %w", err)
		}
		return fmt.Errorf("could not reach LIGHTWEIGHT: %w", err)
	}
	return err
}
