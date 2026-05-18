// DTOs for the user HTTP layer.
//
// Local register/login DTOs are gone — Keycloak owns identity. The /me
// endpoint returns the local projection of the authenticated subject.
package user

import "time"

// UserResponse is the safe view of a local user row.
type UserResponse struct {
	ID          uint      `json:"id"`
	KeycloakSub string    `json:"keycloak_sub"`
	Email       string    `json:"email"`
	Username    string    `json:"username"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ToUserResponse projects a domain User onto its safe wire shape.
func (u *User) ToUserResponse() UserResponse {
	return UserResponse{
		ID:          u.ID,
		KeycloakSub: u.KeycloakSub,
		Email:       u.Email,
		Username:    u.Username,
		CreatedAt:   u.CreatedAt,
		UpdatedAt:   u.UpdatedAt,
	}
}
