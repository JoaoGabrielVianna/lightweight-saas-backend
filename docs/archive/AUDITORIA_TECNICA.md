# Auditoria Técnica — lightweight-saas-backend

## 1. JWT — FATO

**Evidências:**
- **Arquivo:** internal/auth/jwt.go
- **Função:** GenerateJWT, ValidateJWT
- **Biblioteca:** github.com/golang-jwt/jwt/v4
- **Expiração:**
  ```go
  claims := jwt.MapClaims{
      "user_id": userID,
      "exp":     time.Now().Add(time.Hour * 24).Unix(),
  }
  ```
  Expiração de 24 horas.
- **Algoritmo:**
  ```go
  token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
  ```
  Usa HS256 (HMAC SHA-256).

**Conclusão:**
O projeto implementa JWT com expiração de 24h, algoritmo HS256, usando a biblioteca github.com/golang-jwt/jwt/v4.

---

## 2. Testes — FATO

**Evidências:**
- **Arquivo:** internal/user/service_test.go
- **Quantidade:** 1 arquivo *_test.go identificado.
- **Módulos cobertos:** Testes para o service de usuário (autenticação, registro, erros).
- **Exemplo de teste:**
  ```go
  func TestRegisterUser_Success(t *testing.T) { ... }
  func TestRegisterUser_Duplicate(t *testing.T) { ... }
  func TestLoginUser_Success(t *testing.T) { ... }
  func TestLoginUser_WrongPassword(t *testing.T) { ... }
  ```
- **Cobertura:** Testa registro, duplicidade, login, senha errada, erros de repositório.

**Conclusão:**
Há testes unitários cobrindo o service de usuário, focando autenticação e fluxos de erro.

---

## 3. Arquitetura — FATO

**Evidências:**
- **Separação de camadas:**
  - internal/user/handler.go: HTTP handlers
  - internal/user/service.go: lógica de negócio
  - internal/user/repository.go: acesso a dados
- **Fluxo de dados:** handler → service → repository
- **Exemplo:**
  - handler.go:
    ```go
    func RegisterUserHandler(c *gin.Context) {
        var req RegisterUserRequest
        // ...parse e validação...
        user, err := userService.RegisterUser(req.Email, req.Password)
    }
    ```
  - service.go:
    ```go
    func (s *UserService) RegisterUser(email, password string) (*User, error) {
        // ...regra de negócio...
        err := s.repo.CreateUser(user)
    }
    ```
  - repository.go:
    ```go
    func (r *UserRepository) CreateUser(user *User) error {
        return r.db.Create(user).Error
    }
    ```
- **Dependências:** Service depende de Repository via interface. Handler depende de Service.
- **Injeção:** Service recebe Repository por parâmetro (injeção manual).
  ```go
  func NewUserService(repo UserRepository) *UserService
  ```
- **Acoplamento:** Baixo: cada camada só conhece a anterior via interface.

**Conclusão:**
A arquitetura é em camadas, com baixo acoplamento, injeção manual de dependências e fluxo de dados unidirecional.

---

## 4. Hash de senha (bcrypt) — FATO

**Evidências:**
- **Arquivo:** internal/user/service.go
- **Função:** RegisterUser
- **Biblioteca:** golang.org/x/crypto/bcrypt
- **Exemplo:**
  ```go
  hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
  ```
- **Validação:**
  ```go
  err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password))
  ```

**Conclusão:**
Senhas são armazenadas com hash bcrypt, usando o custo padrão.

---

## 5. Middleware de autenticação — FATO

**Evidências:**
- **Arquivo:** internal/auth/middleware.go
- **Função:** AuthMiddleware
- **Exemplo:**
  ```go
  func AuthMiddleware() gin.HandlerFunc {
      return func(c *gin.Context) {
          // ...extrai e valida JWT...
      }
  }
  ```
- **Uso:** internal/server/router.go:
  ```go
  auth := r.Group("/api/users")
  auth.Use(authMiddleware)
  auth.GET("/me", userHandler.MeHandler)
  ```

**Conclusão:**
Rotas protegidas usam middleware que valida JWT.

---

## 6. Banco de dados e ORM — FATO

**Evidências:**
- **Arquivo:** internal/database/database.go
- **Função:** InitDatabase
- **Biblioteca:** gorm.io/gorm, gorm.io/driver/postgres
- **Exemplo:**
  ```go
  db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
  ```

**Conclusão:**
Usa GORM como ORM, banco PostgreSQL.

---

## 7. Variáveis de ambiente — FATO

**Evidências:**
- **Arquivo:** internal/config/config.go
- **Função:** LoadConfig
- **Exemplo:**
  ```go
  viper.BindEnv("PORT")
  viper.BindEnv("DATABASE_URL")
  viper.BindEnv("JWT_SECRET")
  viper.BindEnv("JWT_EXPIRATION")
  ```

**Conclusão:**
Configuração via variáveis de ambiente, usando viper.

---

## 8. Documentação Swagger — FATO

**Evidências:**
- **Arquivo:** docs/swagger.yaml, docs/swagger.json
- **Geração:** README.md:
  ```
  swag init -g cmd/api/main.go
  ```
- **Biblioteca:** github.com/swaggo/gin-swagger

**Conclusão:**
Documentação Swagger gerada automaticamente.

---

## 9. Logging estruturado — FATO

**Evidências:**
- **Arquivo:** internal/logger/logger.go
- **Função:** NewLogger
- **Exemplo:**
  ```go
  log.SetFormatter(&log.JSONFormatter{})
  ```

**Conclusão:**
Logging estruturado em JSON.

---

## 10. Docker Compose — FATO

**Evidências:**
- **Arquivo:** docker-compose.yml
- **Serviço:** postgres:
  ```yaml
  image: postgres:15
  environment:
    POSTGRES_DB: saas_db
    POSTGRES_USER: user
    POSTGRES_PASSWORD: password
  ```

**Conclusão:**
Banco PostgreSQL é orquestrado via Docker Compose.

---

## 11. Endpoints — FATO

**Evidências:**
- **Arquivo:** internal/user/handler.go
- **Funções:** RegisterUserHandler, LoginUserHandler, MeHandler
- **Rotas:**
  - /api/auth/register
  - /api/auth/login
  - /api/users/me

**Conclusão:**
Endpoints REST para registro, login e consulta do usuário autenticado.

---

## 12. Segurança — FATO

**Evidências:**
- **JWT:** Expiração, algoritmo seguro, segredo via env.
- **Senha:** bcrypt.
- **Middleware:** Protege rotas.
- **Validação:** DTOs e validação de request.
- **Exemplo de erro seguro:**
  ```go
  c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
  ```

**Conclusão:**
Boas práticas de segurança implementadas.

---

## 13. Pontos não confirmados (HIPÓTESE)

- **CI/CD:** Não há arquivos .github/workflows, Jenkinsfile, etc. — HIPÓTESE: Não implementado.
- **Rate limiting:** Não há evidência de uso de bibliotecas como "golang.org/x/time/rate" ou similares — HIPÓTESE: Não implementado.
- **Refresh token:** Não há funções ou endpoints relacionados — HIPÓTESE: Não implementado.
- **Multi-tenancy:** Não há modelos ou campos de tenant_id — HIPÓTESE: Não implementado.
- **Monitoramento:** Não há integração com Prometheus, Sentry, etc. — HIPÓTESE: Não implementado.
- **CORS:** Não há configuração explícita de CORS — HIPÓTESE: Não implementado.

---

Se desejar análise de outros pontos, cite o item e aprofundo com evidências do código.
