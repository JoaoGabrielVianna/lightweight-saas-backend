# Monitoring & Observability

**Público:** operadores rodando esta stack em dev, staging, ou como uma fundação reutilizável de IAM.
**Escopo:** o que o binário v0.2.0 já expõe (health, audit logs, eventos de auth, healthchecks de container), no que alertar hoje, e os seams onde uma camada futura de Prometheus / OpenTelemetry vai plugar sem mudanças de código.
**Docs irmãos:** [UPGRADE_AND_ROLLBACK.md](UPGRADE_AND_ROLLBACK.md) para ciclo de vida; [../security/SECURITY_GAPS.md](../security/SECURITY_GAPS.md) para o modelo de ameaça que estes sinais defendem.

---

## 1. Signals at a glance

| Sinal               | De onde vem                                  | Formato                      | Consumidor de hoje                   | Consumidor v0.3+          |
|---------------------|---------------------------------------------|------------------------------|--------------------------------------|---------------------------|
| **Liveness**        | `GET /health`                               | `200 {"status":"ok"}`        | Sonda de container, smoke test       | Liveness probe Kubernetes |
| **Container health**| healthchecks do `docker-compose`            | `healthy / unhealthy`        | `docker ps`, `depends_on`            | Sondas do orquestrador    |
| **Auth events**     | `auth.AuthEvent` → `authEventLogger`        | linha logfmt-ish, `[ auth ]` | `docker logs`, grep                  | Counters Prometheus       |
| **Audit events**    | `audit.Event` → `logging.AuditSink`         | `audit {…JSON…}`, `[ audit ]`| `docker logs`, grep, jq              | Tabela DB + Loki + alerts |
| **Live-admin check**| `auth.RequireLiveAdmin` → `auth.AuthEvent`  | linha `[ auth ] denied`      | Igual aos auth events                | Igual aos auth events     |
| **Gin access log**  | middleware default do gin                   | `[GIN] method | code | dur`  | `docker logs`                        | Promtail → Loki           |
| **Application log** | `logger.Logger` por package origem          | `[ origin ] LEVEL msg`       | `docker logs`                        | Promtail → Loki           |

Tudo abaixo descreve sinais que já estão *plugados* na v0.2.0, salvo quando marcados **futuro** ou **planejado**.

---

## 2. Liveness — `GET /health`

**Endpoint.** `GET http://<api-host>:8080/health` — registrado em [`internal/server/server.go`](../../internal/server/server.go#L143).

**Contrato.**

- Sem auth, sem ping de DB, sem checagem de upstream.
- Retorna `200 {"status":"ok"}` sempre que o processo Gin está aceitando conexões.
- É uma sonda de *liveness*, não de *readiness* — um 200 significa "o processo está no ar", não "o database / Keycloak estão alcançáveis".

**O que ele NÃO te diz.**

- Se o JWKS do Keycloak está obtenível (checado uma vez no startup; se falha, o processo sai — veja `mustBuildAuthProvider` em `cmd/api/main.go:63`).
- Se o pool de conexão do Postgres está saudável.
- Se as credenciais do client da admin API ainda resolvem.

A intenção é que `/health` só caia quando o binário morrer. Para "as dependências estão vivas", use os healthchecks do Docker em §5 mais os sinais de audit log em §4.

**Sonda de exemplo**

```sh
curl -fsS -o /dev/null -w "%{http_code}\n" http://localhost:8080/health
# 200
```

---

## 3. Audit logs — the mutation trail

Cada mutação através de `/admin/*` emite exatamente um evento de auditoria estruturado, independentemente de sucesso ou falha. A invariante de missão para v0.2:

> cada mutação DEVE emitir `who / action / target / timestamp / ip`; falhas DEVEM também emitir `reason`.

### 3.1 Event shape

Definido em [`internal/audit/event.go`](../../internal/audit/event.go):

```jsonc
{
  "action": "user.role_revoked",
  "actor":  {"subject":"ddc2cf1b-…","email":"adminuser@test.com","username":"adminuser"},
  "target": {"kind":"user","id":"2219e074-…","name":"testuser"},
  "ip":     "10.0.0.1",
  "ts":     "2026-05-21T03:14:15Z",
  "reason": "live admin check denied: token role no longer present server-side",
  "extra":  {"roles":["admin"]}
}
```

| Campo    | Obrigatório | Notas |
|----------|:-----------:|-------|
| `action` |  ✓          | Verbo canônico (veja §3.2). Renomear é breaking change. |
| `actor`  |  ✓          | Ao menos um de `subject` / `email` / `username` é populado. Puxado da `Identity` verificada da requisição. |
| `target` |  ✓          | `kind` é curto (`user`, `role`, `session`, `invitation`). `id` é o id canônico (UUID ou nome do role). `name` é label humano opcional. |
| `ts`     |  ✓          | UTC, carimbado por `audit.Record` se zero. |
| `ip`     |  ✓          | O que `gin.Context.ClientIP()` resolver sob a configuração atual de `TrustedProxies`. |
| `reason` | só em falhas | `err.Error()` da camada de serviço. |
| `extra`  | opcional    | Nuance específica do evento — ex.: `{"roles":["editor","support"]}` em concessão de role. |

### 3.2 Canonical actions

Estáveis ao longo de uma versão major — adicionar valores novos é seguro, renomear ou remover quebra todo consumidor downstream.

```
user.created              user.updated              user.deleted
user.roles_granted        user.role_revoked         user.password_reset
role.created              role.updated              role.deleted
session.revoked           user.sessions_logged_out
invitation.created        invitation.resent         invitation.revoked
```

(Fonte de verdade: [`internal/audit/event.go`](../../internal/audit/event.go#L27-L60).)

### 3.3 How it gets to your terminal

O recorder é plugado no boot do processo em [`cmd/api/main.go:44`](../../cmd/api/main.go#L44):

```go
logging.WireDefault()
```

`WireDefault` instala o [`logging.AuditSink`](../../internal/logging/audit_sink.go#L23) como `audit.Recorder` no nível de package. Cada evento vira uma linha no stdout via o logger do projeto com `origin="audit"`:

```
2026-05-21 03:14:15 [44m[97m INFO  [0m [ audit      ] audit {"action":"user.role_revoked","actor":{…},"target":{…},"ip":"10.0.0.1","ts":"2026-05-21T03:14:15Z"}
```

O prefixo `audit ` é fixo em [`internal/logging/audit_sink.go:20`](../../internal/logging/audit_sink.go#L20) — todo filtro downstream deve fazer grep nele.

### 3.4 Useful greps

```sh
# All audit events in the last hour:
docker logs --since 1h saas-api | grep -F '[ audit      ]'

# Only mutations of one user:
docker logs saas-api | grep -F '[ audit      ]' | grep '"id":"2219e074-'

# Only failures (events that carried a reason):
docker logs saas-api | grep -F '[ audit      ]' | grep -F '"reason":'

# Pretty-print:
docker logs saas-api | grep -F '[ audit      ]' | sed 's/.*audit //' | jq -c .
```

### 3.5 What is NOT audited

- Chamadas read-only `GET /admin/*` (listar users, roles, sessions) — não há mutação a registrar. Se você precisa de granularidade de access log, use a linha do gin.
- `/me` e a superfície voltada ao usuário — estas não estão no escopo admin IAM.
- Falhas de `RequireAuth` / `RequireRole` — estas emitem um `AuthEvent` no lugar (§4), não um `Event`.

---

## 4. Auth events — the access-control trail

O middleware em [`internal/auth/middleware.go`](../../internal/auth/middleware.go) emite um `AuthEvent` estruturado para **cada** requisição protegida, sucesso e falha. O hook é registrado em [`cmd/api/main.go:39`](../../cmd/api/main.go#L39).

### 4.1 Event kinds

De [`internal/auth/events.go`](../../internal/auth/events.go):

| Kind                   | Quando |
|------------------------|--------|
| `token_validated`      | Assinatura JWT + issuer + audience todos OK; identidade armazenada no contexto da requisição. |
| `missing_header`       | Sem header `Authorization`. |
| `malformed_header`     | Header não começa com `Bearer ` ou está vazio após o prefixo. |
| `validation_failed`    | Assinatura / issuer / audience / kid-missing / claim `sub` ausente — `reason` carrega o detalhe. |
| `forbidden`            | Token válido mas o chamador não tem a realm role exigida (negação de `RequireRole`) OU a live-admin check os negou (`RequireLiveAdmin` da remediação do GAP-1). |

### 4.2 Line format

O emissor de hoje em `cmd/api/main.go:87`:

```
[ auth ] ok    kind=token_validated sub=<uuid> method=GET path=/me dur=146.374µs
[ auth ] denied kind=forbidden       method=GET path=/admin/users reason=missing role: admin dur=2.1ms
[ auth ] denied kind=forbidden       method=PATCH path=/admin/users/<uuid> reason=live admin check denied: token role no longer present server-side dur=14ms
```

A linha tem formato logfmt (pares `key=value`). Não é JSON — mantida legível para o loop de dev. O hook em si é agnóstico de provedor (`auth.SetEventHook`); reescrever o `authEventLogger` para distribuir para Prometheus ou OpenTelemetry não exige nenhuma mudança no middleware (veja §8).

### 4.3 GAP-1 live-admin denials

A live-admin check ([`internal/auth/admin_check.go:174`](../../internal/auth/admin_check.go)) emite `kind=forbidden` com um `reason=live admin check denied: token role no longer present server-side` distintivo. Trate um destes na natureza como uma **tentativa ativa de exploração** — significa que um token cujas claims ainda diziam `admin` foi rejeitado porque o Keycloak upstream não mostra mais o sujeito como admin (i.e. o usuário foi rebaixado mas o atacante tentou usar o token pré-revogação antes do `exp`). Veja [SECURITY_REMEDIATION_GAP1.md](../security/SECURITY_REMEDIATION_GAP1.md).

Um sinal correlato é `reason=live admin check failed: <upstream-error>`. Esse *não* é um ataque — significa que a API não conseguiu alcançar o Keycloak para verificar. Alerte por taxa, mas leia como disponibilidade, não como segurança.

---

## 5. Container health (docker-compose)

Cinco healthchecks embarcam em [`docker-compose.yml`](../../docker-compose.yml):

| Serviço                  | Sonda                                                                                          | Intervalo / timeout / retries |
|--------------------------|------------------------------------------------------------------------------------------------|-------------------------------|
| `saas-postgres`          | `pg_isready -U $POSTGRES_USER -d $POSTGRES_DB`                                                 | 5s / 5s / 10                  |
| `saas-keycloak-postgres` | `pg_isready -U $KC_DB_USER -d $KC_DB_NAME`                                                     | 5s / 5s / 10                  |
| `saas-mailpit`           | `wget -q --spider http://127.0.0.1:8025/readyz`                                                | 5s / 3s / 10                  |
| `saas-keycloak`          | Fetch em nível TCP de `http://127.0.0.1:9000/health/ready` (a porta de management do Keycloak) | 10s / … / …                   |
| `saas-api`               | *(sem healthcheck container-side)* — protegido por `depends_on: saas-keycloak: condition: service_healthy` |               |

`saas-api` não tem healthcheck próprio container-side porque `/health` já é coberto por sondas externas (smoke test, sonda k8s). A ordem de boot é forçada por `depends_on … condition: service_healthy` em Keycloak + Postgres, de modo que a API nunca sobe contra um realm não-pronto.

**Verifique a qualquer momento:**

```sh
docker ps --format 'table {{.Names}}\t{{.Status}}'
# saas-postgres            Up 35 minutes (healthy)
# saas-keycloak-postgres   Up 35 minutes (healthy)
# saas-keycloak            Up 35 minutes (healthy)
# saas-mailpit             Up 35 minutes (healthy)
# saas-api                 Up 35 minutes
```

Um serviço em `(unhealthy)` por >3 checks consecutivos é seu primeiro page.

---

## 6. Reading the logs in dev

Todos os serviços logam em stdout/stderr; `docker logs` é a interface universal.

```sh
# Tail the API:
docker logs -f saas-api

# Last 5 minutes of Keycloak's WARN/ERROR:
docker logs --since 5m saas-keycloak 2>&1 | grep -E " WARN | ERROR "

# Find every 4xx and 5xx response from gin in the last hour:
docker logs --since 1h saas-api | grep -E '\[GIN\][^|]*\|\s*[45][0-9]{2}'

# Watch audit + auth events live:
docker logs -f saas-api 2>&1 | grep -E '\[ (audit|auth) +\]'
```

**Filtragem por origem.** Cada linha começa com `[ <origin>     ]` vindo do logger do projeto. Origens de hoje:

```
main          server.go banner + boot
auth          RequireAuth / RequireRole / RequireLiveAdmin
audit         the audit-event sink
identity      identity service (admin handlers)
database      gorm + connection lifecycle
keycloak      auth provider (JWKS / token validation)
```

Faça grep em `[ auth ` / `[ audit ` / `[ identity ` para fatiar o sinal que você quer.

**Rotação de logs.** Logs de container crescem para sempre por default. Em produção, configure um driver de rotação em `docker-compose.yml`:

```yaml
services:
  saas-api:
    logging:
      driver: json-file
      options:
        max-size: "50m"
        max-file: "5"
```

Não configurado no compose de dev para manter o boot rápido; **configure antes de embarcar**.

---

## 7. Metrics (current state)

**A v0.2.0 não embarca endpoint `/metrics` nem contadores de métrica.** O middleware emite eventos estruturados que *alimentariam* métricas, mas não há collector de Prometheus plugado ainda.

O que isso significa na prática:

- "Quantos 5xxs em `/admin/users` na última hora?" — só responsível por `docker logs | grep`. Cardinalidade e retenção ficam à mercê do driver de logging do docker.
- "Qual é o p95 de `/me`?" — o access log do gin carrega duração por requisição mas não há agregação em histograma.
- "Quantas negações de live-admin por minuto?" — a mesma coisa.

Decisão deliberada: embarcar os eventos primeiro, o coletor depois. As shapes (§3.1 e §4.1) são estáveis; a camada Prometheus futura (§8) é aditiva e não vai exigir trocar nenhum handler ou middleware.

**Até lá,** o mais próximo de uma métrica é uma query de log estruturado — veja §6 e §9.

---

## 8. Future Prometheus / OpenTelemetry

A base de código é intencionalmente moldada para uma camada de observabilidade aditiva. Três seams existem hoje; trabalho futuro pluga collectors neles sem tocar o hot path da requisição.

### 8.1 Seam — `auth.SetEventHook`

A assinatura em [`internal/auth/events.go:51`](../../internal/auth/events.go#L51):

```go
func SetEventHook(h EventHook) EventHook
```

O hook atual (`authEventLogger`) escreve linhas de log. Um hook futuro pode distribuir:

```go
// pseudo-code, NOT in v0.2.0
func metricsEventHook(e auth.AuthEvent) {
    authReqTotal.With(prom.Labels{
        "kind":   string(e.Kind),
        "method": e.Method,
        "path":   e.Path,
    }).Inc()
    authReqDur.With(prom.Labels{"kind": string(e.Kind)}).Observe(e.Duration.Seconds())
}
auth.SetEventHook(metricsEventHook)
```

Counters/histograms iniciais sugeridos:

| Métrica (planejada)                           | Tipo      | Labels                              |
|-----------------------------------------------|-----------|-------------------------------------|
| `saas_auth_requests_total`                    | counter   | `kind`, `method`, `path_template`   |
| `saas_auth_validation_duration_seconds`       | histogram | `kind`                              |
| `saas_auth_live_admin_check_denied_total`     | counter   | `subject` (ou só count, dada a preocupação com cardinalidade) |
| `saas_auth_live_admin_check_failed_total`     | counter   | `reason_class`                      |

### 8.2 Seam — `audit.SetDefault`

Mesmo padrão em [`internal/audit/recorder.go:42`](../../internal/audit/recorder.go#L42):

```go
func SetDefault(r Recorder) Recorder
```

Hoje: `logging.AuditSink` escreve uma linha de log por evento. Um recorder composto futuro pode distribuir para múltiplos sinks:

```go
// pseudo-code, NOT in v0.2.0
audit.SetDefault(audit.MultiRecorder(
    logging.NewAuditSink(),     // keep the log line
    metrics.AuditCounter(),     // bump prom counters
    audit.NewDBRecorder(db),    // persist to audit_log table
))
```

Counters iniciais sugeridos:

| Métrica (planejada)                           | Tipo    | Labels        |
|-----------------------------------------------|---------|---------------|
| `saas_audit_events_total`                     | counter | `action`      |
| `saas_audit_failures_total`                   | counter | `action`      |

O contrato da interface `Recorder` garante que tanto a distribuição quanto a persistência em DB são aditivas.

### 8.3 Seam — gin middleware

Adicionar um endpoint `/metrics` e um middleware de histograma por requisição é um único mount em `internal/server/server.go`:

```go
// pseudo-code, NOT in v0.2.0
r.Use(prommiddleware.New())
r.GET("/metrics", gin.WrapH(promhttp.Handler()))
```

Esse é o único passo que toca código; os hooks de evento existentes não precisam de mudança nenhuma uma vez que ele esteja instalado.

### 8.4 OpenTelemetry

Os eventos de audit/auth já carregam os campos necessários para iniciar spans (`Path`, `Method`, `Subject`, `Duration`). Um hook de tracing futuro é o irmão natural do hook de métricas — mesmo seam, consumidor diferente.

---

## 9. Alerts — what to page on today

Sem Prometheus, alertas são baseados em log. As shapes abaixo são escritas como `docker logs … | grep` primeiro porque é o que funciona *hoje*; cada linha também anota a query Prometheus futura que vai substituí-la.

| # | Condição                                                                                              | Detector de hoje                                                                                                            | Severidade | Query Prom futura                                       |
|---|-------------------------------------------------------------------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------|------------|---------------------------------------------------------|
| 1 | Container `saas-api` derrubado ou `/health` ≠ 200 por >1 min                                          | sonda externa + `docker ps` mostra ausência ou loop de Restart                                                              | page       | `up{job="saas-api"} == 0`                                |
| 2 | Qualquer serviço do compose `(unhealthy)` por ≥3 checks                                               | status do `docker ps`                                                                                                       | page       | container_state                                          |
| 3 | Eventos `live admin check denied` vistos *de forma alguma*                                            | `docker logs saas-api \| grep -F 'live admin check denied'`                                                                 | page       | `rate(saas_auth_live_admin_check_denied_total[5m]) > 0` |
| 4 | Eventos `live admin check failed` a uma taxa >0.1/s sustentada (Keycloak inalcançável a partir da API)| `docker logs --since 5m saas-api \| grep -c 'live admin check failed' > 30`                                                 | page       | `rate(saas_auth_live_admin_check_failed_total[5m]) > 0.1` |
| 5 | Burst de `kind=validation_failed` (>10/s) — sondagem de forja de token                                | `docker logs --since 1m saas-api \| grep -c 'kind=validation_failed' > 600`                                                  | warn       | `rate(saas_auth_requests_total{kind="validation_failed"}[1m]) > 10` |
| 6 | Pico da taxa de `kind=forbidden` a partir de um único IP — sondagem de RBAC                           | grep por IP (sem agregação hoje)                                                                                            | warn       | `topk(5, rate(saas_auth_requests_total{kind="forbidden"}[5m])) by (ip)` |
| 7 | Lockouts de brute-force vistos no Keycloak (conta temporariamente desabilitada — coberto em [SECURITY_VALIDATION_v0.3.md](../security/SECURITY_VALIDATION_v0.3.md)) | event log de admin do próprio Keycloak + padrão de repetição `Invalid user credentials`                              | warn       | counter on KC admin events                              |
| 8 | Eventos de audit cessaram por >5 min enquanto o tráfego continua                                      | `docker logs --since 5m saas-api \| grep -c '\[ audit'  == 0` AND  linhas gin > 0                                            | page       | `absent_over_time(saas_audit_events_total[5m])`         |
| 9 | Taxa de gin 5xx >1/s por 5 min                                                                        | `docker logs --since 5m saas-api \| grep -cE '\[GIN\][^|]*\| 5[0-9]{2}'`                                                     | page       | `rate(http_requests_total{status=~"5.."}[5m]) > 1`      |

Até que #1, #2, #3 tenham detectores programáticos, trate-os como itens do **runbook MUST monitor**.

---

## 10. Smoke / synthetic checks

Duas sondas já em árvore servem como sinais de saúde:

- [`scripts/security_live_check.sh`](../../scripts/security_live_check.sh) — 17 sondagens de guarda. Sai com não-zero em qualquer drift do contrato 401/403/200. Seguro de rodar em um cron de 1 minuto em staging.
- [`scripts/security_advanced_check.sh`](../../scripts/security_advanced_check.sh) — 6 sondagens avançadas de ameaça. Mais pesado (≈60 s, inclui um gatilho de brute-force que tranca o usuário de teste). Adequado para um cron de 30–60 min, não de 1 minuto.

Ambos escrevem evidência em `docs/evidence/security/`. Plugue-os em seu CI / monitor externo conforme necessário.

---

## 11. Privacy / log hygiene

- O `Actor` de audit carrega `email` e `username`. Se seu deployment trata estes como PII, ou configure o driver de log para enviar a um store ciente de PII, ou wrappeie o `AuditSink` (via `audit.SetDefault`) com um redactor antes de embarcar logs off-host.
- A linha de auth-event carrega o `sub` do JWT (um UUID Keycloak — não PII por si só).
- A linha de auth-event **não** carrega o bearer token, o email do usuário, ou o corpo da requisição.
- O mapa `extra` do evento de audit pode carregar contexto sensível se um handler enfiar algo lá — revise cada chamada `RecordMutation` se você está endurecendo para compliance.

---

## 12. Quick reference card

```
liveness            curl -sS http://localhost:8080/health
container health    docker ps --format 'table {{.Names}}\t{{.Status}}'
audit events        docker logs saas-api | grep -F '[ audit      ]' | sed 's/.*audit //' | jq -c
auth events         docker logs saas-api | grep -F '[ auth       ]'
live-admin denials  docker logs saas-api | grep -F 'live admin check denied'
guard smoke         bash scripts/security_live_check.sh
adversarial smoke   bash scripts/security_advanced_check.sh
```

---

## 13. What this document is NOT

- Um runbook. O "o que fazer quando dispara" mora em `docs/operations/RUNBOOK.md` (próximo sprint).
- Um guia de integração com SIEM. As shapes em §3 / §4 são estáveis; parsing/forwarding do lado do SIEM é deixado à preferência do operador.
- Uma alegação de que o sistema tem métricas. Não tem, ainda — isso é §7 e §8.

Combine com [UPGRADE_AND_ROLLBACK.md](UPGRADE_AND_ROLLBACK.md) para o ciclo de vida e [../security/SECURITY_GAPS.md](../security/SECURITY_GAPS.md) para o modelo de ameaça que estes sinais defendem.
