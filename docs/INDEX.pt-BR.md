# Índice da Documentação

**Propósito:** Ponto único de entrada para cada documento longo deste
repositório. A real fonte de verdade do comportamento é o código e a
especificação OpenAPI gerada ([`swagger.yaml`](swagger.yaml) /
[`swagger.json`](swagger.json)); os documentos catalogados aqui
registram *por que* certas decisões foram tomadas, *o que* foi
validado e *o que* permanece em aberto.

Se você é novo no repositório: comece pelo [`../README.md`](../README.md),
depois pelo [Quick Start](#quick-start) abaixo, depois por
[`getting-started/KEYCLOAK_SETUP.md`](getting-started/KEYCLOAK_SETUP.md), e então volte aqui para o
histórico das milestones.

---

## Navigation

```
docs/
├── INDEX.md                      ← you are here
├── getting-started/QUICKSTART.md                 ← linear path: clone → run → first admin call
├── archive/QUICKSTART_REVIEW.md       ← DX audit of getting-started/QUICKSTART.md (accuracy/consistency)
├── getting-started/KEYCLOAK_SETUP.md             ← onboarding, env vars, troubleshooting
├── architecture/                 ← config-as-source-of-truth design + breaking-change records
│   ├── bootstrap.md
│   └── PHASE3_BREAKING_CHANGE.md
├── docs.go / swagger.json/yaml   ← generated OpenAPI (do not hand-edit)
├── audit/                        ← audit subsystem: model, wiring, operations, validation
├── operations/                   ← operator runbooks (backup, upgrade, monitoring)
├── release/                      ← per-release reports, checklists, tag freezes
├── security/                     ← gap audits, remediations, regressions, validations,
│                                   secrets management, audit operations
├── validation/                   ← functional / smoke / audit / CRUD validation evidence
├── ui/                           ← admin-console UX catalog + dev playground guide
├── roadmap/                      ← known limitations + post-tag hardening backlog
└── evidence/                     ← raw artifacts (api/, screenshots/, security/, ...)
```

---

## Quick Start

Caminho de zero a stack rodando, para engenheiros clonando o repositório.
Combine com [`getting-started/KEYCLOAK_SETUP.md`](getting-started/KEYCLOAK_SETUP.md) para a configuração
do realm e com [`architecture/bootstrap.md`](architecture/bootstrap.md) para o modelo de
config-como-fonte-de-verdade.

| Doc | Escopo |
|-----|--------|
| [`getting-started/QUICKSTART.md`](getting-started/QUICKSTART.md) | Passo a passo linear: instalação → docker-compose → Keycloak → bootstrap → primeira chamada admin. ~10 min. |
| [`archive/QUICKSTART_REVIEW.md`](archive/QUICKSTART_REVIEW.md) | Registro de auditoria de DX do `getting-started/QUICKSTART.md` — verificações factuais cruzadas com `Makefile`, `docker-compose.yml`, `.env.example`, `config/project.json`, `realm-export.json`. Registra o que foi corrigido e o que permanece com lacuna (operações/segredos — cobertos abaixo). |

Assim que a stack estiver no ar, prossiga para as seções [Operations](#operations)
e [Security](#security-reports) para endurecimento de produção.

---

## Operations

Runbooks de operador para rodar a stack além de `make up`. Cada doc é
executável via copy-paste contra o `docker-compose.yml` distribuído e
cruza referências com o backlog de segurança em
[`security/SECURITY_GAPS.md`](security/SECURITY_GAPS.md) e com o roadmap
pós-tag em [`roadmap/HARDENING_REPORT.md`](roadmap/HARDENING_REPORT.md).

| Doc | Escopo |
|-----|--------|
| [`operations/BACKUP_AND_RECOVERY.md`](operations/BACKUP_AND_RECOVERY.md) | Backup e restauração para as duas instâncias do Postgres (app + Keycloak), export/import do realm, simulação de disaster recovery. Cross-link: recuperação de usuário órfão de convite em [`validation/BUG_REPORT_CRUD.md`](validation/BUG_REPORT_CRUD.md) §I14b. |
| [`operations/UPGRADE_AND_ROLLBACK.md`](operations/UPGRADE_AND_ROLLBACK.md) | Procedimento de upgrade por componente (api, Keycloak, Postgres), rollback para `v0.1.0-auth-foundation`, histórico de mudanças incompatíveis em [`architecture/PHASE3_BREAKING_CHANGE.md`](architecture/PHASE3_BREAKING_CHANGE.md). |
| [`operations/MONITORING.md`](operations/MONITORING.md) | Endpoints de saúde, logs estruturados de audit/auth para alertar, fingerprint de negação live-admin do GAP-1, ganchos futuros de Prometheus/OTel. Lê [`security/SECURITY_REMEDIATION_GAP1.md`](security/SECURITY_REMEDIATION_GAP1.md) para a semântica do marcador. |
| [`operations/KEYCLOAK_EMAIL_THEME.md`](operations/KEYCLOAK_EMAIL_THEME.md) | Estado do tema de email customizado (Corsi Enterprise), configuração de SMTP, limitação de persistência entre deploys e plano de solução definitiva via imagem Docker customizada. **Pendência operacional em aberto.** |

Para fluxos específicos de inspeção de audit log, veja
[`audit/AUDIT_OPERATIONS.md`](audit/AUDIT_OPERATIONS.md).

---

## Release history

| Milestone | Notas | Checklist | Congelamentos de tag |
|-----------|-------|-----------|----------------------|
| `v0.1.0-auth-foundation` (2026-05-17) | Tag inicial. Auth delegada ao Keycloak, provisionamento JIT de usuários, pipeline de bootstrap. | — | — |
| `v0.2.0` (2026-05-20) — Identity Management | [`release/RELEASE_v0.2.md`](release/RELEASE_v0.2.md) · [`release/FINAL_RELEASE_REPORT.md`](release/FINAL_RELEASE_REPORT.md) | [`release/RELEASE_CHECKLIST.md`](release/RELEASE_CHECKLIST.md) · [`release/RC1_REPORT.md`](release/RC1_REPORT.md) | [`release/FINAL_TAG_REPORT.md`](release/FINAL_TAG_REPORT.md) (pré-stash → `SAFE_TO_TAG=false`) → [`release/FINAL_TAG_REPORT_v2.md`](release/FINAL_TAG_REPORT_v2.md) (pós-stash → `SAFE_TO_TAG=true`) |

Sign-off funcional por release: [`release/FINAL_SMOKE.md`](release/FINAL_SMOKE.md).
O changelog canônico fica na raiz do repositório: [`../CHANGELOG.md`](../CHANGELOG.md).

---

## Security reports

Sondagens adversariais, análise de lacunas e evidências de remediação
para a superfície de Identity Management, mais os runbooks de segredos
e auditoria que operadores precisam pós-tag em produção.

### Adversarial reports

| Doc | Escopo |
|-----|--------|
| [`security/SECURITY_VALIDATION_v0.2.md`](security/SECURITY_VALIDATION_v0.2.md) | 17 sondagens de guarda black-box (G01–G17). |
| [`security/SECURITY_VALIDATION_v0.3.md`](security/SECURITY_VALIDATION_v0.3.md) | Sondagens avançadas em 6 superfícies (rate-limit, brute force, fixation, replay, concorrência, escalação). |
| [`security/SECURITY_GAPS.md`](security/SECURITY_GAPS.md) | Catálogo de lacunas adversariais. GAP-1 (HIGH, corrigido), GAP-2 (MED, aberto), GAP-3 (LOW, aberto), GAP-4 (INFO, aberto). |
| [`security/SECURITY_REMEDIATION_GAP1.md`](security/SECURITY_REMEDIATION_GAP1.md) | Design + implementação da correção do GAP-1 (`auth.RequireLiveAdmin` + `CachedAdminChecker`). |
| [`security/SECURITY_REGRESSION_GAP1.md`](security/SECURITY_REGRESSION_GAP1.md) | Regressão adversarial pós-correção (R1–R7 PASS). |
| [`security/FINAL_SECURITY.md`](security/FINAL_SECURITY.md) | Veredicto do portão de segurança — síntese dos itens acima. |

### Operator runbooks

| Doc | Escopo |
|-----|--------|
| [`security/SECRETS_MANAGEMENT.md`](security/SECRETS_MANAGEMENT.md) | Inventário de segredos de produção (variáveis de `.env.example`, credenciais do realm-export, bloco SMTP, chaves de assinatura gerenciadas pelo Keycloak), procedimentos de rotação e trade-offs vs. cofres cloud-native. Combine com [`operations/UPGRADE_AND_ROLLBACK.md`](operations/UPGRADE_AND_ROLLBACK.md) ao rotacionar durante uma release. |
| [`audit/AUDIT_OPERATIONS.md`](audit/AUDIT_OPERATIONS.md) | Runbook de inspeção do subsistema de auditoria — "quem fez o quê em `/admin/*`". Construído sobre o modelo em [`audit/AUDIT_EVENTS.md`](audit/AUDIT_EVENTS.md) e o inventário de wiring em [`audit/AUDIT_WIRING.md`](audit/AUDIT_WIRING.md). Combine com [`operations/MONITORING.md`](operations/MONITORING.md) para a camada de alerta. |

Evidência bruta: [`evidence/security/`](evidence/security).

---

## Validation reports

Validação funcional, CRUD, smoke e de emissão de auditoria. Material de
sign-off para os portões de release.

| Doc | Escopo |
|-----|--------|
| [`validation/VALIDATION_PHASE3.md`](validation/VALIDATION_PHASE3.md) | Sign-off do Sprint 3 (entrega ao Keycloak). |
| [`validation/SMOKE_TEST_v0.2.md`](validation/SMOKE_TEST_v0.2.md) | Smoke pass do RC1. |
| [`validation/CRUD_VALIDATION.md`](validation/CRUD_VALIDATION.md) | Validação end-to-end de CRUD (35/35). |
| [`validation/BUG_REPORT_CRUD.md`](validation/BUG_REPORT_CRUD.md) | QA destrutivo (71 checks, 1 defeito corrigido: I14b). |
| [`validation/INVITATION_RELIABILITY_v0.2.md`](validation/INVITATION_RELIABILITY_v0.2.md) | Confiabilidade do ciclo de vida de convites + stress de paginação. |
| [`audit/AUDIT_EVENTS.md`](audit/AUDIT_EVENTS.md) | Modelo de evento de auditoria e vocabulário de ações. |
| [`audit/AUDIT_WIRING.md`](audit/AUDIT_WIRING.md) | Inventário de emissão de auditoria por handler. |
| [`audit/AUDIT_VALIDATION.md`](audit/AUDIT_VALIDATION.md) | Validação end-to-end de emissão de auditoria (PASS). |

Evidência bruta: [`evidence/crud/`](evidence/crud), [`evidence/final/`](evidence/final), [`evidence/api/`](evidence/api).

---

## UI

| Doc | Escopo |
|-----|--------|
| [`ui/UI_BUGS.md`](ui/UI_BUGS.md) | Catálogo de análise estática de `web/admin/` (20 bugs: 2 P0, 4 P1, 7 P2, 7 P3). |
| [`ui/DEV_AUTH_PLAYGROUND.md`](ui/DEV_AUTH_PLAYGROUND.md) | Playground de auth dev-only em `/dev/auth` — fluxos, gate de env, troubleshooting. |

---

## Roadmap

Trabalho futuro — lacunas reconhecidas no momento da release e o
backlog de endurecimento pós-tag.

| Doc | Escopo |
|-----|--------|
| [`roadmap/KNOWN_LIMITATIONS.md`](roadmap/KNOWN_LIMITATIONS.md) | Limitações herdadas do RC1 (backlog de segurança, observabilidade, residual de convite). |
| [`roadmap/HARDENING_REPORT.md`](roadmap/HARDENING_REPORT.md) | Backlog de endurecimento pós-v0.2.0 — consolida referências a todo relatório de validation/security/UI/audit. |

O [`../archive/AUDITORIA_TECNICA.md`](archive/AUDITORIA_TECNICA.md) (em português) no
nível raiz é a auditoria técnica original que precedeu a milestone v0.2.
Mantido na raiz do repositório por visibilidade histórica; não é
relinkado dentro da subárvore.

---

## Evidence

Artefatos brutos — respostas JSON, logs de console, screenshots, saídas
de sondagens de segurança. Linkados pelo relatório que os produziu; não
são navegados isoladamente.

```
evidence/
├── api/               REST responses captured during exploratory probes
├── crud/              CRUD E2E run — api/, api_validation/, mailpit/, network/, screenshots/
├── crud-bugs/         destructive CRUD pass (api/, repro/, ui/)
├── final/             release-gate evidence (auth/, crud/, go/, security/, smoke/)
├── screenshots/       admin-console smoke screenshots (01..09_*.png)
└── security/          advanced/, checks/, gaps/ (incl. remediation/), regression/, summary.txt
```

---

## Duplicate-report audit (2026-05-21)

Conduzido como parte desta limpeza; recomendações são apenas
consultivas.

| Par | Status | Razão | Recomendação |
|------|--------|--------|----------------|
| [`release/FINAL_TAG_REPORT.md`](release/FINAL_TAG_REPORT.md) ↔ [`release/FINAL_TAG_REPORT_v2.md`](release/FINAL_TAG_REPORT_v2.md) | Não são duplicatas | Snapshots sequenciais do mesmo portão. v1 = pré-stash, `SAFE_TO_TAG=false`. v2 = pós-stash, `SAFE_TO_TAG=true`. Ambos são citados por [`roadmap/HARDENING_REPORT.md`](roadmap/HARDENING_REPORT.md) como a trilha canônica de auditoria do freeze. | **Manter ambos.** Não são redundantes; deletar v1 apagaria o registro do portão reprovado que motivou o stash. |
| [`security/SECURITY_VALIDATION_v0.2.md`](security/SECURITY_VALIDATION_v0.2.md) ↔ [`security/SECURITY_VALIDATION_v0.3.md`](security/SECURITY_VALIDATION_v0.3.md) | Não são duplicatas | v0.2 = 17 sondagens baseline de guarda; v0.3 = 6 sondagens avançadas de superfície de ameaça posteriores à v0.2. A v0.3 explicitamente estende a v0.2. | **Manter ambos.** |
| [`audit/AUDIT_EVENTS.md`](audit/AUDIT_EVENTS.md) ↔ [`audit/AUDIT_WIRING.md`](audit/AUDIT_WIRING.md) ↔ [`audit/AUDIT_VALIDATION.md`](audit/AUDIT_VALIDATION.md) | Não são duplicatas | Modelo / inventário de wiring / validação de emissão — três camadas do mesmo subsistema. | **Manter os três.** |
| [`security/FINAL_SECURITY.md`](security/FINAL_SECURITY.md) ↔ [`release/FINAL_SMOKE.md`](release/FINAL_SMOKE.md) ↔ [`release/FINAL_RELEASE_REPORT.md`](release/FINAL_RELEASE_REPORT.md) | Portões distintos | Portão de segurança vs. portão funcional vs. sign-off combinado de release. | **Manter os três.** |

Nenhuma ação de merge / arquivamento foi executada — o grafo de
relatórios existente é narrado por
[`roadmap/HARDENING_REPORT.md`](roadmap/HARDENING_REPORT.md) e quebrar
esse grafo perderia contexto.

---

## Conventions

- Nomes de arquivo são **MAIÚSCULOS + snake_case** para relatórios de
  milestone (ex.: `FINAL_SMOKE.md`) e **lowercase** para docs de design
  perenes (ex.: `architecture/bootstrap.md`). Preservados como estão durante esta
  reorganização.
- Links internos usam **caminhos relativos**: um doc em `security/`
  linka para um irmão em `validation/` via `../validation/FILE.md`.
- Caminhos de evidência não são links em uma superfície de navegação —
  são citações. Trate-os como imutáveis uma vez escritos.
- Arquivos gerados (`docs.go`, `swagger.json`, `swagger.yaml`) são
  produzidos por `make docs` e protegidos por `make swagger-check`.
  Nunca edite à mão; reorganize-os apenas se o caminho de saída do
  gerador também mudar.
