# Lightweight SaaS Backend

> **Status: v0.1.0 — MVP Ready**

A production-ready SaaS backend built with Go and Gin. Designed to be clean, scalable, and easy to understand. Features secure user authentication, request validation, and a layered architecture that separates concerns effectively.

## Overview

This project provides a solid foundation for building SaaS applications. It implements core features—user authentication, secure password handling, JWT-based authorization—with a clean architecture that separates HTTP handlers, business logic, and data access layers.

The goal is simple: provide a real-world backend structure without unnecessary complexity. Every design decision reflects practical SaaS requirements.

## Features

- **User Authentication** — Secure registration and login with bcrypt password hashing
- **JWT Authorization** — Token-based authentication with 24-hour expiration
- **Protected Routes** — Authentication middleware for endpoint protection
- **Current User Endpoint** — `/me` endpoint to retrieve authenticated user data
- **PostgreSQL Integration** — GORM-based ORM with automatic migrations and seed data
- **Comprehensive Testing** — Unit tests for authentication service with 90%+ coverage
- **API Documentation** — Swagger/OpenAPI with auto-generated docs
- **Clean Architecture** — Handler → Service → Repository pattern for maintainability
- **DTO Pattern** — Request/response separation from domain models
- **Structured Logging** — Production-ready logging configuration

## Architecture

The application follows a **feature-first, layered architecture**:

```
HTTP Request
    ↓
Handler (HTTP layer - parsing, validation)
    ↓
Service (business logic - authentication, hashing, validation)
    ↓
Repository (data access - database operations)
    ↓
Database
```

Each feature is organized as a domain package containing:
- **handler** — HTTP endpoint handlers and request validation
- **service** — Business logic and domain rules
- **repository** — Database operations and queries
- **model** — Domain entities
- **dto** — HTTP request/response contracts

This structure keeps concerns isolated and makes features easy to test and extend.

## Project Structure

```
.
├── cmd/
│   └── api/
│       └── main.go                 # Application entry point
├── internal/
│   ├── auth/
│   │   ├── jwt.go                  # JWT token generation and validation
│   │   └── middleware.go           # Authentication middleware
│   ├── user/
│   │   ├── handler.go              # HTTP handlers
│   │   ├── service.go              # Business logic
│   │   ├── service_test.go         # Unit tests
│   │   ├── repository.go           # Database operations
│   │   ├── model.go                # Domain model
│   │   └── dto.go                  # Request/response DTOs
│   ├── config/
│   │   └── config.go               # Configuration loader
│   ├── database/
│   │   └── database.go             # Database initialization
│   ├── logger/
│   │   └── logger.go               # Structured logging
│   └── server/
│       ├── server.go               # Server initialization
│       └── router.go               # Route definitions
├── docs/                           # Swagger/OpenAPI documentation
├── docker-compose.yml              # Docker Compose for PostgreSQL
├── go.mod                          # Go module definition
└── README.md                       # This file
```

## Quick Start

### Prerequisites
- Go 1.21+
- Docker and Docker Compose
- PostgreSQL 15+ (via Docker Compose)

### Installation

1. **Clone the repository**
```bash
git clone https://github.com/yourusername/lightweight-saas-backend.git
cd lightweight-saas-backend
```

2. **Start PostgreSQL**
```bash
docker-compose up -d
```

3. **Install dependencies**
```bash
go mod download
```

4. **Run the application**
```bash
go run cmd/api/main.go
```

The API will be available at `http://localhost:8080`.

API documentation is available at `http://localhost:8080/swagger/index.html`.

## API Usage

### User Registration

Register a new user account:

```bash
curl -X POST http://localhost:8080/api/auth/register \
  -H "Content-Type: application/json" \
  -d '{
    "email": "user@example.com",
    "password": "securePassword123"
  }'
```

Response:
```json
{
  "id": 1,
  "email": "user@example.com",
  "created_at": "2026-04-04T10:30:00Z"
}
```

### User Login

Authenticate and receive a JWT token:

```bash
curl -X POST http://localhost:8080/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{
    "email": "user@example.com",
    "password": "securePassword123"
  }'
```

Response:
```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "expires_in": 86400
}
```

### Get Current User

Retrieve authenticated user information using the JWT token:

```bash
curl -X GET http://localhost:8080/api/users/me \
  -H "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
```

Response:
```json
{
  "id": 1,
  "email": "user@example.com",
  "created_at": "2026-04-04T10:30:00Z",
  "updated_at": "2026-04-04T10:30:00Z"
}
```

### Test with Wrong Credentials

```bash
curl -X POST http://localhost:8080/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{
    "email": "user@example.com",
    "password": "wrongPassword"
  }'
```

Response (401):
```json
{
  "error": "invalid credentials"
}
```

## API Documentation

Complete API documentation is auto-generated and available at:

```
http://localhost:8080/swagger/index.html
```

Endpoints are documented with examples, request/response schemas, and authentication requirements.

To regenerate Swagger documentation after making changes:

```bash
swag init -g cmd/api/main.go
```

## Authentication Flow

1. **User Registration** — Email and plaintext password provided
   - Password is hashed using bcrypt (cost: 10)
   - User is stored in the database
   - User ID and timestamps are returned

2. **User Login** — Email and plaintext password provided
   - User is retrieved from the database by email
   - Plaintext password is compared against stored bcrypt hash
   - On success, JWT token is generated with 24-hour expiration
   - Token is returned to client

3. **Protected Requests** — Client sends JWT in Authorization header
   - Middleware extracts and validates JWT token
   - Token claims are extracted (particularly user ID)
   - Request context is enriched with user information
   - Handler accesses user from context

4. **Token Expiration** — After 24 hours, token becomes invalid
   - Client receives 401 Unauthorized
   - User logs in again to receive new token

## Testing

Run all tests:

```bash
go test ./...
```

Run tests with coverage:

```bash
go test ./... -cover
```

Run specific package tests:

```bash
go test ./internal/user -v
```

Coverage breakdown:
- **Authentication service**: 90%+ coverage
- **Password hashing**: Tested (bcrypt integration)
- **JWT validation**: Tested (middleware coverage)
- **Error handling**: Tested (duplicate users, invalid credentials, repository errors)

Test cases:
- ✓ Successful user registration
- ✓ Duplicate user prevention
- ✓ Successful login with correct credentials
- ✓ Login rejection with wrong password
- ✓ Login rejection for non-existent users
- ✓ Repository error handling
- ✓ End-to-end registration and login flow

## Roadmap

### v0.2.0 (In Progress)
- Multi-tenant support with tenant isolation
- Lead management API
- Tenant invitations and role-based access control

### v0.3.0 (Planned)
- Refresh token implementation
- Email verification
- Password reset flow
- OAuth2 integration

### v1.0.0 (Planned)
- Comprehensive audit logging
- Rate limiting
- Advanced permission system
- Production deployment guide

## Engineering Highlights

### Clean Architecture
The codebase separates concerns into distinct layers:
- **Handlers** know about HTTP but not database
- **Services** contain business logic and don't depend on frameworks
- **Repositories** handle only data access

This makes the code testable, maintainable, and framework-agnostic.

### Security Best Practices
- Passwords are hashed using bcrypt with automatic cost adjustment
- JWT tokens have explicit expiration times
- Sensitive data (passwords) is excluded from JSON responses
- Authentication middleware validates all protected routes
- Error messages don't leak sensitive information

### Production Readiness
- Structured logging for observability
- Comprehensive error handling
- Database connection pooling via GORM
- Automatic schema migrations
- Request validation at the HTTP layer
- Graceful shutdown handling

### Testing Strategy
- Unit tests for core business logic
- Mock repository implementation (no external testing libraries)
- Integration-style tests for complete workflows
- Simple, maintainable test structure

### Database Design
- Automatic migrations keep schema in sync with code
- Unique constraints on email prevent duplicates
- Timestamps (created_at, updated_at) for audit trails
- Proper indexing on frequently queried fields

## Tech Stack

| Component | Technology |
|-----------|------------|
| Language | Go 1.21+ |
| Web Framework | Gin |
| Database | PostgreSQL 15+ |
| ORM | GORM |
| Authentication | JWT (golang-jwt) |
| Password Hashing | bcrypt |
| API Documentation | Swagger/OpenAPI (swaggo) |
| Logging | Structured (encoding/json) |
| Testing | Go standard library |

## Environment Variables

Create a `.env` file with the following variables:

```
PORT=8080
DATABASE_URL=postgres://user:password@postgres:5432/saas_db
JWT_SECRET=your-secret-key-change-in-production
JWT_EXPIRATION=86400
```

See `.env.example` for a complete example.

## Contributing

This project is designed as a reference implementation for SaaS backend architectures. While it's not accepting contributions, you're welcome to fork it and adapt it for your own projects.

## About

This project demonstrates production-ready backend engineering with Go. It's built to serve as a reference for:
- Clean architecture patterns in Go
- Secure authentication implementation
- Professional code organization
- Testing practices
- Real-world SaaS backend design

The focus is on code quality, maintainability, and practical design decisions that scale.

## License

MIT