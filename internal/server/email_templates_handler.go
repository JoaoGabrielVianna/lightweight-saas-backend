package server

import (
	"errors"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity"
	identitykc "github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/identity/keycloak"
	"github.com/gin-gonic/gin"
)

const emailTemplatesLocale = "en"

// knownTemplates are the Keycloak message keys exposed for editing.
// Keys match the default keycloak theme message bundle exactly.
var knownTemplates = []templateDef{
	{
		Key:         "executeActionsEmailSubject",
		Label:       "Convite — Assunto",
		Description: "Assunto do email enviado quando um usuário é convidado.",
		Kind:        "text",
	},
	{
		Key:         "executeActionsEmailBodyHtml",
		Label:       "Convite — Corpo (HTML)",
		Description: "Corpo HTML do email de convite. Preserve: ${link}, ${realmName}, ${user.firstName}.",
		Kind:        "html",
	},
	{
		Key:         "passwordResetSubject",
		Label:       "Reset de Senha — Assunto",
		Description: "Assunto do email de reset de senha.",
		Kind:        "text",
	},
	{
		Key:         "passwordResetBodyHtml",
		Label:       "Reset de Senha — Corpo (HTML)",
		Description: "Corpo HTML do email de reset de senha. Preserve: ${link}, ${realmName}, ${user.firstName}.",
		Kind:        "html",
	},
	{
		Key:         "emailVerificationSubject",
		Label:       "Verificação de Email — Assunto",
		Description: "Assunto do email de verificação de endereço.",
		Kind:        "text",
	},
	{
		Key:         "emailVerificationBodyHtml",
		Label:       "Verificação de Email — Corpo (HTML)",
		Description: "Corpo HTML do email de verificação. Preserve: ${link}, ${realmName}, ${user.firstName}.",
		Kind:        "html",
	},
}

type templateDef struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Kind        string `json:"kind"`
}

type templateEntry struct {
	templateDef
	Value    string `json:"value"`
	Override bool   `json:"override"`
}

// EmailTemplatesHandler exposes admin REST endpoints for managing Keycloak
// realm email message overrides via the localization API.
type EmailTemplatesHandler struct {
	provider *identitykc.Provider
}

// NewEmailTemplatesHandler wraps a keycloak provider. Returns nil when
// provider is nil so the router can guard route registration with a nil check.
func NewEmailTemplatesHandler(p *identitykc.Provider) *EmailTemplatesHandler {
	if p == nil {
		return nil
	}
	return &EmailTemplatesHandler{provider: p}
}

// GetEmailTemplates returns the known template definitions merged with any
// current overrides stored in Keycloak's localization API.
//
// GET /admin/settings/email-templates
func (h *EmailTemplatesHandler) GetEmailTemplates(c *gin.Context) {
	overrides, err := h.provider.GetLocalization(c.Request.Context(), emailTemplatesLocale)
	if err != nil {
		c.JSON(502, gin.H{"error": gin.H{"message": "keycloak unreachable: " + err.Error()}})
		return
	}

	entries := make([]templateEntry, len(knownTemplates))
	for i, t := range knownTemplates {
		val, ok := overrides[t.Key]
		entries[i] = templateEntry{
			templateDef: t,
			Value:       val,
			Override:    ok,
		}
	}
	c.JSON(200, gin.H{"templates": entries, "locale": emailTemplatesLocale})
}

// UpdateEmailTemplate sets a single message key override in Keycloak.
//
// PUT /admin/settings/email-templates/:key
func (h *EmailTemplatesHandler) UpdateEmailTemplate(c *gin.Context) {
	key := c.Param("key")
	if !isKnownTemplateKey(key) {
		c.JSON(400, gin.H{"error": gin.H{"message": "unknown template key: " + key}})
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": gin.H{"message": "invalid body: " + err.Error()}})
		return
	}
	// Localization overrides only take effect when realm internationalization is
	// enabled. Enable it transparently on first save — safe, additive operation.
	_ = h.provider.EnableInternationalizationIfNeeded(c.Request.Context())

	if err := h.provider.SetLocalizationKey(c.Request.Context(), emailTemplatesLocale, key, body.Value); err != nil {
		c.JSON(502, gin.H{"error": gin.H{"message": "keycloak: " + err.Error()}})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// ResetEmailTemplate removes the custom override for a key, reverting to the
// theme default.
//
// DELETE /admin/settings/email-templates/:key
func (h *EmailTemplatesHandler) ResetEmailTemplate(c *gin.Context) {
	key := c.Param("key")
	if !isKnownTemplateKey(key) {
		c.JSON(400, gin.H{"error": gin.H{"message": "unknown template key: " + key}})
		return
	}
	err := h.provider.DeleteLocalizationKey(c.Request.Context(), emailTemplatesLocale, key)
	if err != nil && !errors.Is(err, identity.ErrNotFound) {
		c.JSON(502, gin.H{"error": gin.H{"message": "keycloak: " + err.Error()}})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func isKnownTemplateKey(key string) bool {
	for _, t := range knownTemplates {
		if t.Key == key {
			return true
		}
	}
	return false
}
