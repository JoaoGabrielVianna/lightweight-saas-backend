// =====================================================
// Service handles user business logic and authentication operations.
//
// This service layer implements core business logic for user authentication
// including registration and login. It sits between HTTP handlers and the
// repository layer, handling password hashing with bcrypt and validation.
//
// Usage:
//
//	service := user.NewService(repo)
//	user, err := service.Register("user@example.com", "password123")
//	if err != nil {
//	    log.Fatal(err)
//	}
//
// =====================================================

package user

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

var (
	// ErrUserAlreadyExists is returned when attempting to register with an existing email.
	ErrUserAlreadyExists = errors.New("user already exists")

	// ErrInvalidCredentials is returned when login credentials are incorrect.
	ErrInvalidCredentials = errors.New("invalid credentials")
)

// =====================================================
// Service handles user business logic.
//
// This struct wraps the user repository and provides high-level
// operations like registration and login with proper validation
// and password hashing.
//
// Fields:
//   - repo: The user repository for database operations
//
// =====================================================
type Service struct {
	repo UserRepository
}

// =====================================================
// NewService creates a new user service.
//
// This constructor initializes a service with the provided
// repository. The repository should be fully configured
// and ready for database operations.
//
// Parameters:
//   - repo: A user repository instance
//
// Returns:
//   - A new Service instance
//
// =====================================================
func NewService(repo UserRepository) *Service {
	return &Service{repo: repo}
}

// =====================================================
// Register creates a new user account.
//
// This method validates that the email doesn't already exist,
// hashes the password using bcrypt, and stores the user in
// the database.
//
// Parameters:
//   - email: The user's email address
//   - password: The plaintext password (will be hashed)
//
// Returns:
//   - *User: The created user with ID and timestamps populated
//   - error: ErrUserAlreadyExists if email is taken,
//     other errors if hashing or database operations fail
//
// Example:
//
//	user, err := service.Register("user@example.com", "securepass123")
//	if err != nil {
//	    if err == ErrUserAlreadyExists {
//	        fmt.Println("Email already registered")
//	    } else {
//	        log.Fatal(err)
//	    }
//	}
//	fmt.Printf("User created with ID: %d\n", user.ID)
//
// =====================================================
func (s *Service) Register(email string, password string) (*User, error) {
	// Check if user already exists
	existingUser, err := s.repo.FindByEmail(email)
	if err != nil {
		return nil, err
	}

	if existingUser != nil {
		return nil, ErrUserAlreadyExists
	}

	// Hash the password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	// Create the user
	user := &User{
		Email:    email,
		Password: string(hashedPassword),
	}

	if err := s.repo.Create(user); err != nil {
		return nil, err
	}

	return user, nil
}

// =====================================================
// Login authenticates a user by email and password.
//
// This method finds the user by email and compares the provided
// password with the stored hash using bcrypt. Returns the user
// if credentials are valid.
//
// Parameters:
//   - email: The user's email address
//   - password: The plaintext password to verify
//
// Returns:
//   - *User: The authenticated user
//   - error: ErrInvalidCredentials if email not found or password is wrong,
//     other errors if database operations fail
//
// Example:
//
//	user, err := service.Login("user@example.com", "securepass123")
//	if err != nil {
//	    if err == ErrInvalidCredentials {
//	        fmt.Println("Invalid email or password")
//	    } else {
//	        log.Fatal(err)
//	    }
//	}
//	fmt.Printf("Login successful for user: %s\n", user.Email)
//
// =====================================================
func (s *Service) Login(email string, password string) (*User, error) {
	// Find user by email
	user, err := s.repo.FindByEmail(email)
	if err != nil {
		return nil, err
	}

	// Check if user exists
	if user == nil {
		return nil, ErrInvalidCredentials
	}

	// Compare password with hash
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}

	return user, nil
}

// =====================================================
// GetByID retrieves a user by their primary key.
//
// This method is used for fetching user data in authenticated requests
// where the user ID is known (extracted from JWT token).
//
// Parameters:
//   - id: The user's primary key
//
// Returns:
//   - *User: The user if found
//   - error: ErrInvalidCredentials if user not found,
//     other errors if database operations fail
//
// Example:
//
//	user, err := service.GetByID(1)
//	if err != nil {
//	    if err == ErrInvalidCredentials {
//	        fmt.Println("User not found")
//	    } else {
//	        log.Fatal(err)
//	    }
//	}
//	fmt.Printf("User: %s\n", user.Email)
//
// =====================================================
func (s *Service) GetByID(id uint) (*User, error) {
	user, err := s.repo.FindByID(id)
	if err != nil {
		return nil, err
	}

	if user == nil {
		return nil, ErrInvalidCredentials
	}

	return user, nil
}
