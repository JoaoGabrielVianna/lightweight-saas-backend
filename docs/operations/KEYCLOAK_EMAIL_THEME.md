# Keycloak — Email Theme (Corsi Enterprise)

## Estado atual

O realm `corsi-enterprise` usa o tema customizado `corsi`, configurado via Admin API:

```json
{ "emailTheme": "corsi" }
```

Os arquivos FTL estão em `deploy/keycloak/themes/corsi/email/` no repositório e são copiados manualmente para o container em produção.

### Templates disponíveis

| Arquivo | Disparo |
|---|---|
| `html/executeActions.ftl` + `text/executeActions.ftl` | Convite / ativação de conta |
| `html/password-reset.ftl` + `text/password-reset.ftl` | Reset de senha |
| `html/email-verification.ftl` + `text/email-verification.ftl` | Verificação de email |

### SMTP

Configurado via IAM (`POST /admin/settings/smtp`) usando Zoho Mail (`smtp.zoho.com:587`, STARTTLS). Credenciais gerenciadas no painel do IAM — nenhuma variável de ambiente necessária em produção.

---

## Problema pendente — tema não persiste entre deploys

O Keycloak roda no EasyPanel via Docker Swarm. A cada deploy ou restart do container, os arquivos de tema em `/opt/keycloak/themes/corsi/` são perdidos.

**Workaround atual:** copiar os arquivos manualmente via `docker exec` após cada restart.

```bash
CONTAINER=$(docker ps | grep keycloak | grep -v db | awk '{print $NF}')
docker exec $CONTAINER mkdir -p /opt/keycloak/themes/corsi/email/html /opt/keycloak/themes/corsi/email/text
# copiar cada .ftl do deploy/keycloak/themes/corsi/ para o container
```

---

## Solução definitiva — imagem customizada

Criar um `Dockerfile.keycloak` que inclua o tema no build:

```dockerfile
FROM quay.io/keycloak/keycloak:24.0
COPY deploy/keycloak/themes/corsi /opt/keycloak/themes/corsi
```

Passos para implementar:

1. Criar `deploy/keycloak/Dockerfile` com o conteúdo acima.
2. Adicionar build e push da imagem no GitHub Actions (workflow separado ou no CI existente).
3. No EasyPanel, trocar a imagem do serviço Keycloak de `quay.io/keycloak/keycloak:24.0` para a imagem publicada no GHCR (ex: `ghcr.io/org/keycloak-corsi:latest`).
4. Configurar o webhook de deploy para disparar o rebuild da imagem a cada push em `deploy/keycloak/**`.

Enquanto isso não for feito, o tema precisa ser copiado manualmente toda vez que o container for recriado.

---

## Limitação da Admin API

O Keycloak não expõe endpoint REST para upload de arquivos de tema. Por isso:

- **O que o IAM consegue configurar:** SMTP, `emailTheme` do realm, assuntos/textos via localization API (apenas para login/account pages — não para emails).
- **O que requer acesso ao filesystem:** os arquivos `.ftl` do tema de email.

A solução definitiva com imagem customizada elimina a necessidade de acesso manual ao servidor.
