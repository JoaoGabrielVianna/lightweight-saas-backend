// =====================================================
// Package user handles user domain logic and persistence.
//
// This package provides user authentication operations with proper error
// handling and GORM integration. It follows a clean architecture pattern
// focused on the current authentication flow needs.
//
// Usage:
//
//	repo := user.NewRepository(db)
//	user, err := repo.FindByEmail("user@example.com")
//	if err != nil {
//	    log.Fatal(err)
//	}
//
// =====================================================
package user

import (
	"errors"

	"gorm.io/gorm"
)

// =====================================================
// UserRepository defines the interface for user persistence operations.
//
// This interface allows for flexible repository implementations,
// making it easy to mock repository behavior in tests.
//
// =====================================================
type UserRepository interface {
	Create(user *User) error
	FindByEmail(email string) (*User, error)
	FindByID(id uint) (*User, error)
}

// =====================================================
// Repository handles user persistence operations.
//
// This struct wraps a GORM database connection and provides
// a clean interface for all user-related database operations.
//
// Fields:
//   - db: The GORM database connection
//
// =====================================================
type Repository struct {
	db *gorm.DB
}

// =====================================================
// NewRepository creates a new user repository.
//
// This constructor initializes a repository with the provided
// GORM database connection. The connection should be fully
// configured and ready for use.
//
// Parameters:
//   - db: An initialized GORM database connection
//
// Returns:
//   - A new Repository instance
//
// =====================================================
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// =====================================================
// Create inserts a new user into the database.
//
// This method saves a new user record to the users table.
// GORM automatically handles timestamps (CreatedAt, UpdatedAt).
//
// Parameters:
//   - user: Pointer to the User struct to insert
//
// Returns:
//   - error: Nil if successful, error otherwise
//
// Example:
//
//	user := &User{Email: "user@example.com", Password: "hashed_pw"}
//	if err := repo.Create(user); err != nil {
//	    log.Fatal(err)
//	}
//
// =====================================================
func (r *Repository) Create(user *User) error {
	return r.db.Create(user).Error
}

// =====================================================
// FindByEmail retrieves a user by email address.
//
// This method queries the database for a user with the given email.
// Returns (nil, nil) if the user is not found.
//
// Parameters:
//   - email: The user's email address
//
// Returns:
//   - *User: Pointer to the User if found, nil otherwise
//   - error: Nil if successful, error if database error occurs
//
// Example:
//
//	user, err := repo.FindByEmail("user@example.com")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	if user == nil {
//	    fmt.Println("User not found")
//	}
//
// =====================================================
func (r *Repository) FindByEmail(email string) (*User, error) {
	var user User
	result := r.db.Where("email = ?", email).First(&user)

	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, nil
	}

	if result.Error != nil {
		return nil, result.Error
	}

	return &user, nil
}

// =====================================================
// FindByID retrieves a user by their primary key.
//
// This method queries the database for a user with the given ID.
// Returns (nil, nil) if the user is not found.
//
// Parameters:
//   - id: The user's primary key
//
// Returns:
//   - *User: Pointer to the User if found, nil otherwise
//   - error: Nil if successful, error if database error occurs
//
// Example:
//
//	user, err := repo.FindByID(1)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	if user == nil {
//	    fmt.Println("User not found")
//	}
//
// =====================================================
func (r *Repository) FindByID(id uint) (*User, error) {
	var user User
	result := r.db.First(&user, id)

	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, nil
	}

	if result.Error != nil {
		return nil, result.Error
	}

	return &user, nil
}
