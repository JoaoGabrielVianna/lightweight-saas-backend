package identity

import "errors"

// Sentinel errors. Concrete providers wrap these (via fmt.Errorf with %w)
// so callers can errors.Is on the kind without knowing the implementation.
//
// HTTP mapping handled centrally by the handler layer:
//
//	ErrNotFound            -> 404
//	ErrBadRequest          -> 400
//	ErrForbidden           -> 403
//	ErrConflict            -> 409
//	ErrNotConfigured       -> 503 (admin credentials missing at boot)
//	ErrAdminAPIUnavailable -> 502 (upstream Keycloak failed / network / 5xx)
//	other                  -> 500
var (
	ErrNotFound            = errors.New("identity: not found")
	ErrBadRequest          = errors.New("identity: bad request")
	ErrForbidden           = errors.New("identity: forbidden")
	ErrConflict            = errors.New("identity: conflict")
	ErrNotConfigured       = errors.New("identity: admin client credentials not configured")
	ErrAdminAPIUnavailable = errors.New("identity: admin API unavailable")
)
