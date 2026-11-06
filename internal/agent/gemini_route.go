package agent

import (
	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
)

// Routing Gemini CLI through the gateway rewrites four values: the base URL
// and API key in ~/.gemini/.env, and the auth type and model in settings.json.
// Before the first rewrite the originals go to the stash (empty ones are
// stored as absent), so unrouting can put back exactly what was there.

const geminiAuthAPIKey = "gemini-api-key"

const (
	geminiBaseKey = "GOOGLE_GEMINI_BASE_URL"
	geminiKeyKey  = "GEMINI_API_KEY"
	geminiAuthKey = "security.auth.selectedType"
	geminiModel   = "model.name"
)

type geminiRoute struct {
	settings string // ~/.gemini/settings.json
	env      string // ~/.gemini/.env
}

// routed reports whether Gemini CLI currently points at this gateway.
func (g geminiRoute) routed() bool {
	v, _ := edit.GetEnvFile(g.env, geminiBaseKey)
	return v == gateway.URL()
}

// route points Gemini CLI at the gateway for a catalog model. The original
// values are stashed only when not already routed, so a second route never
// overwrites what the user had before.
func (g geminiRoute) route(model string) error {
	if !g.routed() {
		base, _ := edit.GetEnvFile(g.env, geminiBaseKey)
		key, _ := edit.GetEnvFile(g.env, geminiKeyKey)
		auth, _ := edit.GetJSON(g.settings, geminiAuthKey)
		cur, _ := edit.GetJSON(g.settings, geminiModel)
		stash(map[string]string{
			"gemini.base_url": base,
			"gemini.api_key":  key,
			"gemini.auth":     auth,
			"gemini.model":    cur,
		})
	}
	if err := edit.SetEnvFile(g.env,
		edit.KV{Path: geminiBaseKey, Value: gateway.URL()},
		edit.KV{Path: geminiKeyKey, Value: gateway.Token},
	); err != nil {
		return err
	}
	return edit.SetJSON(g.settings,
		edit.KV{Path: geminiAuthKey, Value: geminiAuthAPIKey},
		edit.KV{Path: geminiModel, Value: model},
	)
}

// unroute puts back the stashed originals. Values that were absent before
// routing are removed. It does nothing when Gemini CLI is not routed.
func (g geminiRoute) unroute() error {
	if !g.routed() {
		return nil
	}
	if err := envBack(g.env, geminiBaseKey, unstash("gemini.base_url")); err != nil {
		return err
	}
	if err := envBack(g.env, geminiKeyKey, unstash("gemini.api_key")); err != nil {
		return err
	}
	if err := jsonBack(g.settings, geminiAuthKey, unstash("gemini.auth")); err != nil {
		return err
	}
	return jsonBack(g.settings, geminiModel, unstash("gemini.model"))
}

func envBack(path, key, val string) error {
	if val == "" {
		return edit.DelEnvFile(path, key)
	}
	return edit.SetEnvFile(path, edit.KV{Path: key, Value: val})
}

func jsonBack(path, key, val string) error {
	if val == "" {
		return edit.DelJSON(path, key)
	}
	return edit.SetJSON(path, edit.KV{Path: key, Value: val})
}
