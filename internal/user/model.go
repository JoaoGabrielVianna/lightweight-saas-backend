package user

import (
	"time"
)

// User is the local projection of an authenticated identity from the auth
// provider (Keycloak). The provider owns credentials; this row mirrors the
// claims this API cares about so the rest of the platform (relations,
// audits, FKs, future features) can reference users by a stable uint id
// without depending on opaque external subject strings.
//
// Uniqueness invariant: KeycloakSub is the canonical identity. The unique
// index enforces "never duplicate" at the DB level even under concurrent
// first-login races; the Service.EnsureUser flow retries lookup on
// constraint violation.
type User struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	KeycloakSub string    `gorm:"uniqueIndex;not null" json:"keycloak_sub"`
	Email       string    `gorm:"index" json:"email"`
	Username    string    `gorm:"not null" json:"username"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TableName specifies the table name for the User model.
func (User) TableName() string {
	return "users"
}
