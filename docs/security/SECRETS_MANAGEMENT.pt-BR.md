# Production Secrets Management

**Público:** operadores subindo esta stack fora da topologia `docker-compose` de dev — primariamente deployments single-host em VPS.
**Escopo:** cada segredo que este projeto efetivamente tem. Especificamente as variáveis em [.env.example](../../.env.example), as credenciais dentro de [deploy/keycloak/realm-export.json](../../deploy/keycloak/realm-export.json), o bloco SMTP no realm, e as chaves de assinatura JWT gerenciadas pelo Keycloak.
**Fora do escopo:** lógica de aplicação, comportamento de authn/authz (veja [docs/security/FINAL_SECURITY.md](FINAL_SECURITY.md)), e stores de segredos cloud-native como AWS Secrets Manager / Vault — cobertos brevemente apenas em "futuro".

---

## 1. Secret inventory

Cada segredo nomeado na base de código hoje, em uma tabela. Se você adicionar um segredo novo em código, adicione-o aqui.

| ID  | Variável / localização                                       | O que destrava                                                                           | Fonte de verdade         | Consumido por                            | Blast radius se vazar                                                                  |
|-----|--------------------------------------------------------------|------------------------------------------------------------------------------------------|--------------------------|------------------------------------------|----------------------------------------------------------------------------------------|
| S1  | `POSTGRES_PASSWORD` (.env)                                   | Auth do superuser do DB da app                                                           | `.env` (gitignored)      | docker-compose `postgres` + app Go via `DB_URL` | Leitura/escrita completa de dados da aplicação; movimento lateral só se a porta do DB for alcançável |
| S2  | `DB_URL` (.env)                                              | Mesma coisa — mas pré-renderizada com credenciais inline                                 | `.env`                   | App Go (string DSN)                      | Igual a S1                                                                             |
| S3  | `KC_DB_PASSWORD` (.env)                                      | Database próprio do Keycloak                                                              | `.env`                   | docker-compose `keycloak-postgres` + `keycloak` | Estado completo do Keycloak (usuários, config de realm, material de chave de assinatura) — pior segredo único |
| S4  | `KEYCLOAK_ADMIN_PASSWORD` (.env)                             | Conta de bootstrap no realm `master` do Keycloak                                          | `.env`                   | startup do `keycloak` no docker-compose  | Controle completo de cada realm na instância do Keycloak                              |
| S5  | `KEYCLOAK_CLIENT_SECRET` (.env)                              | Credenciais do client `saas-backend` (client de validação de token)                      | `.env` + `realm-export.json` | App Go                                | Emissão de tokens impersonando o client da API                                         |
| S6  | `KEYCLOAK_ADMIN_CLIENT_SECRET` (.env)                        | Service account `saas-backend-admin` (chama a Admin REST API do Keycloak)                | `.env` + `realm-export.json` | App Go (superfície `/admin/*`)        | Ações de admin no realm `saas`: ler/criar/deletar usuários, sessões, roles            |
| S7  | `SEED_USER_PASSWORD` (.env)                                  | Senhas iniciais para usuários em `realm-export.json` (`testuser`, `adminuser`)            | `.env`                   | Apenas no import do realm                | Login como usuários de teste semeados — mas estes não devem existir em prod (veja §6.1) |
| S8  | Chaves de assinatura do realm Keycloak (HS256/RS256/EdDSA)   | Assinaturas JWT — a única coisa em que a API confia ao validar bearer tokens             | DB do Keycloak (S3)      | Apenas Keycloak; a app verifica via JWKS  | Forjar qualquer access token; impersonar qualquer usuário, incluindo admins            |
| S9  | `smtpServer.password` do realm (atualmente vazio em dev)     | Credenciais do relay SMTP para convite / reset de senha / verify-email                   | Config do realm (Admin UI) | Apenas Keycloak                        | Spam de saída do seu relay; reputação queimada; possível vazamento de PII via headers |
| S10 | Certificados TLS + chaves privadas (reverse proxy / Keycloak)| Criptografia de cada conexão                                                              | Fora do `.env` — veja §6.3 | nginx / Caddy / Traefik na frente da API + Keycloak | Eavesdropping passivo; MITM rebaixado                                              |

O `.env.example` atual embarca **defaults de dev que NÃO devem ser usados em produção** — veja §6 para o checklist de endurecimento.

---

## 2. The `.env` file

`.env` é o store canônico de segredos em runtime. Está gitignored ([`.gitignore` linha 2](../../.gitignore)). Trate-o como a senha da sua casa.

### 2.1 Generation, not editing

O arquivo é **regerado** por `cmd/bootstrap` a partir de [config/project.json](../../config/project.json) mais os valores de segredos existentes. O cabeçalho do banner diz isso:

```
# Auto-generated by `make init` / cmd/bootstrap. Edit config/project.json
# (and re-run `make regen`) rather than editing this file by hand.
# Secrets are sourced from this .env at regeneration time and preserved.
```

Consequência prática: **nunca coloque um segredo apenas no `.env`** — coloque também no seu backup seguro. O próximo `make regen` preservará valores existentes, mas um `.env` perdido é um segredo perdido.

### 2.2 File-system hardening on a VPS

O `.env` default de desenvolvimento mora ao lado da árvore de código-fonte, legível para qualquer um que possa fazer `cat` nele. Em um host de produção:

```
# Run as the service user, NOT root.
sudo install -o saas -g saas -m 0600 .env /etc/saas/api.env

# Verify
stat -c '%U:%G %a' /etc/saas/api.env   # → saas:saas 600
```

Então aponte a unit / container para a localização protegida:

```ini
# /etc/systemd/system/saas-api.service
[Service]
User=saas
Group=saas
EnvironmentFile=/etc/saas/api.env
ExecStart=/usr/local/bin/saas-api
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
```

Para Docker:

```bash
# Pass the file in — DO NOT bake secrets into the image.
docker run --env-file=/etc/saas/api.env --read-only ...
```

### 2.3 What the .env file MUST NOT do

- Ser comitado (verificado — `.env` está no `.gitignore`).
- Ser legível por qualquer um exceto o usuário de serviço (`chmod 600`, não `644`).
- Aparecer em `ps`/`cmdline`. Passe via `EnvironmentFile=` ou `--env-file`, não `--env KEY=VALUE` inline. Runtimes de container vazam valores inline para qualquer um com `ps` no host.
- Ser back-uppeado sem criptografia. Veja §5.3.
- Ser enviado via chat / sites de paste. Use `pass`, `age`, ou `sops` para handoff entre operadores.

---

## 3. Keycloak secrets

Duas preocupações distintas:

### 3.1 O root admin do realm `master` (`KEYCLOAK_ADMIN` / `KEYCLOAK_ADMIN_PASSWORD`)

**Essa é a credencial de maior valor na stack.** Um usuário com admin do realm `master` pode criar novos realms, conceder a si mesmo qualquer role, exfiltrar chaves de assinatura, etc. Os defaults de dev são literalmente `admin` / `admin`.

- Rotacione **antes do primeiro boot não-local**. Procedimento: suba o Keycloak com os defaults de dev, logue uma vez, crie um novo usuário admin com senha forte e fresca, delete o usuário `admin` de bootstrap e então delete `KEYCLOAK_ADMIN`/`KEYCLOAK_ADMIN_PASSWORD` do seu `.env` de produção.
- O container Keycloak lê essas variáveis de ambiente apenas no **primeiro boot de um database fresco**. Boots subsequentes as ignoram. Deixá-las no `.env` após o primeiro boot é mais cosmético — mas também é uma credencial grátis que um vazamento de lista de processos entregaria, então zerá-las é mais seguro.

### 3.2 Secrets de client do realm-export (`saas-backend`, `saas-backend-admin`)

`deploy/keycloak/realm-export.json` contém:

```json
{
  "clientId": "saas-backend",
  "secret": "saas-backend-secret"
},
{
  "clientId": "saas-backend-admin",
  "secret": "saas-backend-admin-secret"
}
```

Esses são **placeholders de dev** que combinam com `KEYCLOAK_CLIENT_SECRET` e `KEYCLOAK_ADMIN_CLIENT_SECRET` em `.env.example`. Eles são comitados no git de propósito para que a stack de dev suba sem prompt.

**O padrão de realm-export não sobrevive ao contato com produção.** Opções para prod, em ordem de preferência:

1. **Não importe o realm de dev de jeito nenhum.** Exporte o realm de dev, substitua cada `secret`/`password` por placeholders do estilo `${ENV_VAR}`, importe via `kc.sh import --override true` com envsubst pré-processado. O arquivo de realm export no git fica dev-only.
2. **Importe dev uma vez, depois mute.** Suba o realm a partir do export comitado, depois imediatamente rotacione cada client secret via Admin REST API (ou `kcadm.sh regenerate secret` por client). Salve os secrets novos no seu `.env` de produção.
3. **Mantenha um `realm-export.prod.json` paralelo** com placeholders, comitado mas inutilizável como está. A pipeline de deploy substitui placeholders antes de passar ao Keycloak.

Qualquer que seja sua escolha: **os valores atualmente em `realm-export.json` nunca devem ser os valores que seu realm de prod usa.**

### 3.3 Service-account scope

A service account `saas-backend-admin` é o que faz `/admin/*` funcionar. Ela precisa das client roles `realm-management` (`manage-users`, `query-users`, `view-users`, `manage-realm`, `view-realm`, `manage-clients`, `view-clients`, `query-clients`, `view-events`). Ela **não** precisa de roles do realm `master`. Audite periodicamente:

```bash
kcadm.sh get-roles --uusername service-account-saas-backend-admin -r saas
```

Se a lista de roles desviar (alguém adicionou `realm-admin` por conveniência), reduza de volta.

---

## 4. JWT secrets

Importante adiantar: **esta aplicação não assina JWTs**. O Keycloak assina. A app verifica via JWKS.

- Material de chave de assinatura ativa mora no **database do Keycloak** (ou seja, dentro do blast radius de S3). A app não detém chave privada.
- `KEYCLOAK_JWKS_URL` no `.env` aponta para o endpoint de chave pública do Keycloak. Essa URL é pública; não é segredo.
- Há uma referência vestigial a `JWT_SECRET` em [internal/config/config.go:138](../../internal/config/config.go#L138) como comentário-doc. **Isto é histórico** — o campo não está plugado, e o fluxo Keycloak configurado usa RS256 assimétrico + JWKS. Se você ver `JWT_SECRET=…` em um `.env`, ele não está fazendo nada.

### 4.1 Key rotation (the part you DO own)

O Keycloak suporta rotação de par de chaves sem disrupção visível ao usuário:

1. **Adicione uma nova chave ativa** em `Realm Settings → Keys → Providers → rsa-generated → Add provider`. Defina `priority` mais alta que a chave atual.
2. O Keycloak começa a assinar tokens de acesso **novos** com a nova chave imediatamente. Tokens existentes ainda verificam contra a chave velha porque o Keycloak a mantém `passive` (ainda servida via JWKS) até você removê-la.
3. Após **accessTokenLifespan** ter passado desde a rotação (atualmente `3600s` por `realm-export.json:2`), todo token em circulação foi re-emitido com a chave nova. A chave velha pode ser deletada com segurança.

Se a rotação é **de emergência** (suspeita de comprometimento), não espere — desabilite a chave velha imediatamente. Cada token assinado com ela instantaneamente se torna inválido, forçando cada usuário ativo a re-autenticar.

### 4.2 Token lifespan policy

Defaults de `deploy/keycloak/realm-export.json`:

```
"accessTokenLifespan": 3600,        // 1 h  — bearer token validity
"ssoSessionIdleTimeout": 1800,      // 30 m — idle → refresh required
"ssoSessionMaxLifespan": 36000      // 10 h — hard cap on a session
```

Esses são confortáveis para dev. Para prod, aperte conforme gostar:

| Configuração              | Dev   | Sugerido prod  | Racional                                                                          |
|---------------------------|------:|---------------:|------------------------------------------------------------------------------------|
| `accessTokenLifespan`     | 3600  | 300–900        | Janela menor para um access token roubado                                          |
| `ssoSessionIdleTimeout`   | 1800  | 900            | Idle → re-auth forçado                                                             |
| `ssoSessionMaxLifespan`   | 36000 | 28800 (8h)     | Limita uma sessão sem atenção                                                       |

Reduzir `accessTokenLifespan` aumenta a carga no endpoint de refresh de token e na recheckagem do live-admin ([internal/auth/admin_check.go](../../internal/auth/admin_check.go)). O TTL do cache é tunável via `ADMIN_LIVE_CHECK_TTL_SECONDS` ([internal/config/config.go:182](../../internal/config/config.go#L182)).

---

## 5. SMTP secrets

### 5.1 Current state (dev)

A stack de dev usa [Mailpit](https://github.com/axllent/mailpit) na porta 1025, sem auth, sem TLS — veja [docker-compose.yml:45-65](../../docker-compose.yml). O bloco SMTP do realm ([deploy/keycloak/realm-export.json:77-85](../../deploy/keycloak/realm-export.json#L77-L85)) reflete isso:

```json
"smtpServer": {
  "host": "mailpit",
  "port": "1025",
  "from": "no-reply@saas.local",
  "fromDisplayName": "lightweight-saas-backend",
  "auth": "false",
  "starttls": "false",
  "ssl": "false"
}
```

**Mailpit é um catch-all. Ele nunca deve rodar em produção.** Ele aceita qualquer auth, encaminha nada, e expõe uma interface web em 8025 que qualquer um com alcance de rede pode ler.

### 5.2 Prod SMTP

Escolha um relay (SES, Postmark, Mailgun, SendGrid, seu ISP, um relay self-hosted). Configure no realm — via Admin UI ou editando o bloco SMTP antes do import do realm:

```json
"smtpServer": {
  "host":            "smtp.example.com",
  "port":            "587",
  "from":            "noreply@yourdomain",
  "fromDisplayName": "your-product",
  "auth":            "true",
  "user":            "${SMTP_USER}",      // substituted at import / set via Admin UI
  "password":        "${SMTP_PASSWORD}",  // never literally in git
  "starttls":        "true",
  "ssl":             "false"
}
```

O campo `password`, uma vez setado via a Admin UI, mora no database do Keycloak (blast radius de S3). Ele **não** é exposto via leituras da Admin REST API — só escritas — então não pode vazar via endpoint admin mal-configurado. Mas PODE vazar via realm-export. Se você algum dia rodar `kc.sh export` num realm de prod, **lavar `smtpServer.password` do export antes de comitar ou compartilhar**.

### 5.3 What an SMTP breach looks like

Spam de saída do seu relay → impacto na reputação do ISP → emails de convite começam a ir para spam dos destinatários → onboarding quebrado. O trabalho de compensating-delete do lado do Keycloak ([docs/INVITATION_RELIABILITY_v0.2.md](../validation/INVITATION_RELIABILITY_v0.2.md)) já lida com falha de SMTP no meio do convite, então uma queda *temporária* é recuperável. Uma *brecha* não é — rotacione a senha SMTP e audite `/admin/invitations` para qualquer entrada não-familiar.

### 5.4 SMTP rotation

A rotação mais barata na stack: mude as credenciais no relay, atualize o bloco SMTP do realm. Não há cadeia de consumidores a coordenar — só a thread de mail do Keycloak lê isso.

---

## 6. Backups (encrypted)

O que quer que detenha `.env` e o volume do DB do Keycloak deve ser back-uppeado com criptografia em repouso. Opções para um deploy single-VPS:

- `restic` para um object store remoto, configurado com `RESTIC_PASSWORD` mantido em uma localização separada (não no mesmo `.env` que você está tentando back-uppear).
- `pg_dump | age -r <key> > backup.age` para o DB do Keycloak — mantenha o recipiente age público no host; a chave privada mora fora do host.
- `sops` para o `.env` em si em um repositório git separado (privado, com ACLs apropriados).

**Teste a restauração ao menos trimestralmente.** Um backup que nunca foi restaurado é um chute.

---

## 7. Rotation cadence

Esses são pisos. Rotacione mais cedo em qualquer suspeita de comprometimento (admin saindo, entrada de access log suspeita, screenshot vazado, etc.).

| Segredo                                                 | Cadência rotineira | Gatilho de emergência                                    | Procedimento                                                                                       |
|---------------------------------------------------------|--------------------|----------------------------------------------------------|----------------------------------------------------------------------------------------------------|
| `KEYCLOAK_ADMIN_PASSWORD` (realm master)                | 90 d               | Saída de admin; qualquer suspeita de vazamento           | Login → criar novo admin → deletar admin velho → zerar var no `.env` após o primeiro boot          |
| `KEYCLOAK_CLIENT_SECRET` (saas-backend)                 | 90 d               | Comprometimento de host API; emissão de token suspeita    | Admin UI Keycloak → Clients → saas-backend → Credentials → Regenerate → atualizar `.env` → reiniciar API |
| `KEYCLOAK_ADMIN_CLIENT_SECRET` (saas-backend-admin)     | 90 d               | Comprometimento de host API                              | Igual ao acima, para `saas-backend-admin`                                                          |
| Chave de assinatura do realm (JWT)                      | 180 d              | Qualquer suspeita de exfiltração de chave                | Admin UI → Realm Settings → Keys → Providers → adicionar nova rsa-generated → esperar `accessTokenLifespan` → deletar velha. Ou desabilitar velha imediatamente para emergência. |
| `POSTGRES_PASSWORD`                                     | 180 d              | Comprometimento de host de DB                            | `ALTER USER postgres WITH PASSWORD '…'` → atualizar `.env` → reiniciar API                         |
| `KC_DB_PASSWORD`                                        | 180 d              | Comprometimento de host KC                               | `ALTER USER keycloak WITH PASSWORD '…'` → atualizar `.env` → reiniciar Keycloak (derruba sessões; avise usuários) |
| `smtpServer.password`                                   | 180 d              | Reclamações de spam; queda de reputação                  | Rotacionar no relay → atualizar via Admin UI                                                       |
| `SEED_USER_PASSWORD`                                    | n/a — deletar      | n/a                                                       | Usuários semeados não devem existir em prod. Verifique com `kcadm.sh get users -r saas -q username=testuser` |
| Certificados TLS                                        | por CA             | Exposição de chave privada                                | Reemitir (auto-renew do Let's Encrypt) → recarregar proxy                                          |
| Passphrase de backup (restic / age / sops)              | anual              | Saída de operador com conhecimento da passphrase          | Re-criptografar backups com nova passphrase; aposentar velha                                       |

Rotacionar S5/S6 **não** invalida JWTs de usuário já emitidos — esses são assinados por S8. A ordem importa:

- Rotação de client-secret força apenas **re-auth de serviço-para-serviço** (app Go ↔ endpoint de token do Keycloak).
- Rotação de chave de assinatura invalida tokens de **usuário final** após eles expirarem.

---

## 8. Pre-deploy hardening checklist

Antes do primeiro `make up` não-local:

- [ ] `.env` existe em `/etc/saas/api.env` (ou equivalente) com `chmod 600`, dono do usuário de serviço, NÃO da árvore de fonte.
- [ ] `POSTGRES_PASSWORD` não é `postgres`.
- [ ] `KC_DB_PASSWORD` não é `keycloak`.
- [ ] `KEYCLOAK_ADMIN` / `KEYCLOAK_ADMIN_PASSWORD` estão unset OU substituídos (tradeoff de primeiro boot — veja §3.1).
- [ ] `KEYCLOAK_CLIENT_SECRET` **não** é `saas-backend-secret`. Regenere via Admin UI.
- [ ] `KEYCLOAK_ADMIN_CLIENT_SECRET` **não** é `saas-backend-admin-secret`. Regenere via Admin UI.
- [ ] `SEED_USER_PASSWORD` é removido; o import do realm foi editado para retirar `testuser` / `adminuser`, OU eles foram deletados via `kcadm.sh` após o import.
- [ ] `smtpServer` do realm aponta para um relay real; `password` está setado; `starttls` é `true`.
- [ ] `sslRequired` do realm é `external` ou `all` (atualmente `none` em `realm-export.json:86`).
- [ ] `bruteForceProtected` do realm é `true` (já assim em dev — verificado por [docs/security/FINAL_SECURITY.md](FINAL_SECURITY.md) §3 T2).
- [ ] `accessTokenLifespan` apertado conforme §4.2.
- [ ] TLS termina na frente tanto da API quanto do Keycloak. Exposição TCP direta de `5432`, `5433`, `8081`, `1025`, `8025` está OFF.
- [ ] `DEV_PLAYGROUND_ENABLED=false`. O playground `/dev/auth` e o console `/admin` não devem ser alcançáveis em prod.
- [ ] `features.identity_management` permanece `true` se você precisa de `/admin/*`, mas o UI do console admin atrás dele é dev-only — coloque gate no proxy.
- [ ] Backups configurados E testados (§6).
- [ ] Todos os operadores com conhecimento do `.env` têm o procedimento de rotação (§7) salvo nos favoritos.

---

## 9. Threat model — what these controls do and don't cover

| Ameaça                                                  | Mitigada por                                | Risco residual                                                                                       |
|---------------------------------------------------------|----------------------------------------------|------------------------------------------------------------------------------------------------------|
| Backup do VPS perdido                                   | §6 backups criptografados + storage remoto    | Se passphrase + backup estão no mesmo lugar, nenhum                                                  |
| Saída de admin descontente                              | §7 rotação; revogar a realm admin role        | Tokens que ele tem são válidos até `accessTokenLifespan` expirar, salvo se a chave de assinatura for rotacionada |
| Vazamento por lista de processos (`ps auxe`)            | `EnvironmentFile=` / `--env-file`             | Nenhum se você não passa `-e KEY=VALUE` inline                                                       |
| Scan do histórico git por `.env` passado                | `.env` sempre gitignored                      | Pre-commit hook (ex. `gitleaks`) pega futuros erros; nada repara vazamentos passados a não ser reescrita de histórico |
| Roubo de bearer token via XSS / clipboard               | `accessTokenLifespan` curto; PKCE no fluxo SPA | Token roubado-e-reapresentado funciona até `accessTokenLifespan` segundos. Recheckagem live-admin ([admin_check.go](../../internal/auth/admin_check.go)) bloqueia tokens admin roubados após rebaixamento |
| DB do Keycloak comprometido                             | Criptografia de filesystem; backup offline    | Game over — veja S3+S8                                                                              |
| Reverse proxy comprometido                              | TLS pinning no downstream (mTLS, opcional)    | Atualmente nenhum — single-host single-proxy é a assunção de design                                  |
| Insider com shell no host da API                        | Rodar API como usuário não-privilegiado; `chmod 600 .env` | Inspeção de memória ainda possível (`gcore`); para isso, você precisa de disco-criptografia + auditd |

---

## 10. What's still gappy

Estas coisas não são tratadas por este documento e seriam as próximas a atacar:

- **Sem gerenciador de segredos central.** Padrão single-VPS mantém tudo em `.env`. Migração para Vault / SOPS-criptografado-no-git / um KMS cloud é iteração futura, não realidade de hoje.
- **Sem hook de secret-scanning em CI.** Adicionar `gitleaks` à cadeia pre-commit pegaria commits acidentais antes de baterem em histórico.
- **`realm-export.json` embarca dev secrets no git.** Aceitável para dev, perigoso se alguém copia o arquivo à mão para um deploy de prod. Veja §3.2 para os três padrões de mitigação.
- **Sem rotação automatizada.** A cadência em §7 é manual. Uma abordagem de scheduled-job futura (cron + `kcadm.sh` para client secrets) fecharia a lacuna de esquecimento humano.
- **Sem runbook de vazamento de segredo.** Este doc cobre rotação; ele não especifica **quem** chamar, **como** revogar, **o que** comunicar quando um vazamento é confirmado. Isso pertence a um documento de incident-response ainda não escrito.

---

## 11. Quick reference

```text
.env path on VPS                 /etc/saas/api.env (chmod 600, owned by service user)
JWT signing model                Keycloak-issued RS256 (or whatever realm is configured), validated via JWKS
Token rotation                   accessTokenLifespan (default 3600s) governs natural expiry
Forced re-auth                   Disable old signing key in Realm Settings → Keys
Client secret rotation           Admin UI → Clients → <id> → Credentials → Regenerate
Admin password rotation          Admin UI → master realm → Users → admin → Credentials → Reset
SMTP credentials                 Realm Settings → Email → Connection & Authentication
Backups encrypted                restic / age / sops, passphrase stored separately
Out-of-band handoff              pass / age / sops; never chat / paste
```
