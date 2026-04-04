package user

import (
	"errors"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// MockRepository is a simple mock implementation of the UserRepository interface.
// It stores users in memory and allows test cases to set up
// specific repository behaviors without external mocking libraries.
type MockRepository struct {
	// users stores users by email for retrieval
	users map[string]*User

	// findByEmailError allows tests to simulate database errors
	findByEmailError error

	// createError allows tests to simulate database errors
	createError error

	// Tracks method calls
	FindByEmailCalls int
	CreateCalls      int
}

// NewMockRepository creates a new mock repository for testing.
func NewMockRepository() *MockRepository {
	return &MockRepository{
		users: make(map[string]*User),
	}
}

// FindByEmail implements UserRepository.FindByEmail
func (m *MockRepository) FindByEmail(email string) (*User, error) {
	m.FindByEmailCalls++

	if m.findByEmailError != nil {
		return nil, m.findByEmailError
	}

	user, exists := m.users[email]
	if !exists {
		return nil, nil
	}

	return user, nil
}

// Create implements UserRepository.Create
func (m *MockRepository) Create(user *User) error {
	m.CreateCalls++

	if m.createError != nil {
		return m.createError
	}

	// Simulate GORM behavior: populate ID
	if user.ID == 0 {
		user.ID = uint(len(m.users) + 1)
	}

	m.users[user.Email] = user
	return nil
}

// FindByID implements UserRepository.FindByID
func (m *MockRepository) FindByID(id uint) (*User, error) {
	for _, user := range m.users {
		if user.ID == id {
			return user, nil
		}
	}
	return nil, nil
}

// TestRegisterSuccess verifies that a new user is successfully registered.
func TestRegisterSuccess(t *testing.T) {
	repo := NewMockRepository()
	service := NewService(repo)

	email := "test@example.com"
	password := "securePassword123"

	user, err := service.Register(email, password)

	if err != nil {
		t.Fatalf("Register failed unexpectedly: %v", err)
	}

	if user == nil {
		t.Fatal("Register returned nil user")
	}

	if user.Email != email {
		t.Errorf("expected email %q, got %q", email, user.Email)
	}

	// Verify password was hashed (should not equal plain password)
	if user.Password == password {
		t.Error("password was not hashed")
	}

	// Verify password hash is valid
	err = bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password))
	if err != nil {
		t.Errorf("password hash verification failed: %v", err)
	}

	// Verify user was stored in repository
	if repo.FindByEmailCalls != 1 {
		t.Errorf("expected FindByEmail to be called once, got %d", repo.FindByEmailCalls)
	}

	if repo.CreateCalls != 1 {
		t.Errorf("expected Create to be called once, got %d", repo.CreateCalls)
	}
}

// TestRegisterUserAlreadyExists verifies that registering with an existing email fails.
func TestRegisterUserAlreadyExists(t *testing.T) {
	repo := NewMockRepository()
	service := NewService(repo)

	email := "existing@example.com"
	password := "password123"

	// Create first user
	user1, err := service.Register(email, password)
	if err != nil {
		t.Fatalf("first Register failed: %v", err)
	}

	if user1 == nil {
		t.Fatal("first Register returned nil user")
	}

	// Try to register with same email
	user2, err := service.Register(email, "differentPassword")

	if err == nil {
		t.Fatal("expected ErrUserAlreadyExists, got nil")
	}

	if !errors.Is(err, ErrUserAlreadyExists) {
		t.Errorf("expected ErrUserAlreadyExists, got %v", err)
	}

	if user2 != nil {
		t.Fatal("expected nil user when registration fails")
	}

	// Verify FindByEmail was called to check for existing user
	if repo.FindByEmailCalls != 2 {
		t.Errorf("expected FindByEmail to be called twice, got %d", repo.FindByEmailCalls)
	}

	// Verify Create was not called for second registration
	if repo.CreateCalls != 1 {
		t.Errorf("expected Create to be called once, got %d", repo.CreateCalls)
	}
}

// TestRegisterRepositoryError verifies error handling when repository fails during user lookup.
func TestRegisterRepositoryError(t *testing.T) {
	repo := NewMockRepository()
	repo.findByEmailError = errors.New("database connection error")
	service := NewService(repo)

	user, err := service.Register("test@example.com", "password123")

	if err == nil {
		t.Fatal("expected error from repository, got nil")
	}

	if user != nil {
		t.Fatal("expected nil user when error occurs")
	}

	if !errors.Is(err, repo.findByEmailError) {
		t.Errorf("expected error %v, got %v", repo.findByEmailError, err)
	}
}

// TestRegisterCreateError verifies error handling when repository fails during user creation.
func TestRegisterCreateError(t *testing.T) {
	repo := NewMockRepository()
	repo.createError = errors.New("failed to insert user")
	service := NewService(repo)

	user, err := service.Register("test@example.com", "password123")

	if err == nil {
		t.Fatal("expected error from Create, got nil")
	}

	if user != nil {
		t.Fatal("expected nil user when error occurs")
	}

	if !errors.Is(err, repo.createError) {
		t.Errorf("expected error %v, got %v", repo.createError, err)
	}
}

// TestLoginSuccess verifies successful login with correct credentials.
func TestLoginSuccess(t *testing.T) {
	repo := NewMockRepository()
	service := NewService(repo)

	email := "user@example.com"
	password := "correctPassword123"

	// Register a user first
	_, err := service.Register(email, password)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Reset call counter for clean test
	repo.FindByEmailCalls = 0

	// Login with correct credentials
	user, err := service.Login(email, password)

	if err != nil {
		t.Fatalf("Login failed unexpectedly: %v", err)
	}

	if user == nil {
		t.Fatal("Login returned nil user")
	}

	if user.Email != email {
		t.Errorf("expected email %q, got %q", email, user.Email)
	}

	// Verify FindByEmail was called
	if repo.FindByEmailCalls != 1 {
		t.Errorf("expected FindByEmail to be called once, got %d", repo.FindByEmailCalls)
	}
}

// TestLoginWrongPassword verifies login fails with incorrect password.
func TestLoginWrongPassword(t *testing.T) {
	repo := NewMockRepository()
	service := NewService(repo)

	email := "user@example.com"
	correctPassword := "correctPassword123"
	wrongPassword := "wrongPassword456"

	// Register a user
	_, err := service.Register(email, correctPassword)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Reset call counter
	repo.FindByEmailCalls = 0

	// Try login with wrong password
	user, err := service.Login(email, wrongPassword)

	if err == nil {
		t.Fatal("expected ErrInvalidCredentials, got nil")
	}

	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}

	if user != nil {
		t.Fatal("expected nil user when login fails")
	}

	// Verify FindByEmail was called
	if repo.FindByEmailCalls != 1 {
		t.Errorf("expected FindByEmail to be called once, got %d", repo.FindByEmailCalls)
	}
}

// TestLoginUserNotFound verifies login fails when user doesn't exist.
func TestLoginUserNotFound(t *testing.T) {
	repo := NewMockRepository()
	service := NewService(repo)

	email := "nonexistent@example.com"
	password := "anyPassword123"

	// Try to login with non-existent user
	user, err := service.Login(email, password)

	if err == nil {
		t.Fatal("expected ErrInvalidCredentials, got nil")
	}

	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}

	if user != nil {
		t.Fatal("expected nil user when user not found")
	}

	// Verify FindByEmail was called
	if repo.FindByEmailCalls != 1 {
		t.Errorf("expected FindByEmail to be called once, got %d", repo.FindByEmailCalls)
	}
}

// TestLoginRepositoryError verifies error handling when repository fails.
func TestLoginRepositoryError(t *testing.T) {
	repo := NewMockRepository()
	repo.findByEmailError = errors.New("database connection failed")
	service := NewService(repo)

	user, err := service.Login("test@example.com", "password123")

	if err == nil {
		t.Fatal("expected error from Login, got nil")
	}

	if user != nil {
		t.Fatal("expected nil user when error occurs")
	}

	if !errors.Is(err, repo.findByEmailError) {
		t.Errorf("expected error %v, got %v", repo.findByEmailError, err)
	}
}

// TestRegisterAndLoginFlow verifies the complete registration and login flow.
func TestRegisterAndLoginFlow(t *testing.T) {
	repo := NewMockRepository()
	service := NewService(repo)

	testCases := []struct {
		name     string
		email    string
		password string
	}{
		{
			name:     "simple credentials",
			email:    "user1@example.com",
			password: "password123",
		},
		{
			name:     "complex password",
			email:    "user2@example.com",
			password: "P@ssw0rd!#$%Complex123",
		},
		{
			name:     "multiple registrations",
			email:    "user3@example.com",
			password: "anotherPassword",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Register
			regUser, err := service.Register(tc.email, tc.password)
			if err != nil {
				t.Fatalf("Register failed: %v", err)
			}

			if regUser.Email != tc.email {
				t.Errorf("Register: expected email %q, got %q", tc.email, regUser.Email)
			}

			// Login with correct password
			loginUser, err := service.Login(tc.email, tc.password)
			if err != nil {
				t.Fatalf("Login failed: %v", err)
			}

			if loginUser.Email != tc.email {
				t.Errorf("Login: expected email %q, got %q", tc.email, loginUser.Email)
			}

			// Login with wrong password should fail
			_, err = service.Login(tc.email, "wrongPassword")
			if err == nil {
				t.Error("Login with wrong password should fail")
			}

			if !errors.Is(err, ErrInvalidCredentials) {
				t.Errorf("expected ErrInvalidCredentials, got %v", err)
			}
		})
	}
}
