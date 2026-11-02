package agent

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yetone/dial/internal/catalog"
	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

// Gemini CLI only speaks Google's own API, so "provider" here is how it
// authenticates: Google sign-in, a Gemini API key, or Vertex AI. The choice
// lives in settings.json (security.auth.selectedType). A Google provider
// added in dial lends its key through ~/.gemini/.env, which the CLI loads.

func gemini(home string) *Agent {
	dir := filepath.Join(home, ".gemini")
	path := filepath.Join(dir, "settings.json")
	envPath := filepath.Join(dir, ".env")
	auth := jsonGet(path, "security.auth.selectedType")
	base := func() string { v, _ := edit.GetEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL"); return v }
	envKey := func() string { v, _ := edit.GetEnvFile(envPath, "GEMINI_API_KEY"); return v }
	dialKey := func() string {
		for _, id := range []string{"google", "gemini"} {
			if p, err := provider.Find(id); err == nil && p.Key != "" {
				return p.Key
			}
		}
		return ""
	}

	// Routing through dial: GOOGLE_GEMINI_BASE_URL points at the gateway
	// (the CLI appends /v1beta/models/…), GEMINI_API_KEY is the gateway token
	// and the CLI is told to use an API key. What was there before is stashed
	// once — present or absent — and put back exactly when routing ends.
	routed := func() bool { return strings.TrimRight(base(), "/") == gateway.URL() }
	route := func(model string) error {
		if !routed() {
			saved := map[string]string{geminiStash["routed"]: "1"}
			if v, ok := edit.GetEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL"); ok {
				saved[geminiStash["base"]] = "=" + v
			}
			if v, ok := edit.GetEnvFile(envPath, "GEMINI_API_KEY"); ok {
				saved[geminiStash["key"]] = "=" + v
			}
			if v, ok := edit.GetJSON(path, "security.auth.selectedType"); ok {
				saved[geminiStash["auth"]] = "=" + v
			}
			if v, ok := edit.GetJSON(path, "model.name"); ok {
				saved[geminiStash["model"]] = "=" + v
			}
			forget(geminiStashKeys()...)
			stash(saved)
		}
		if err := edit.SetEnvFile(envPath,
			edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: gateway.URL()},
			edit.KV{Path: "GEMINI_API_KEY", Value: gateway.Token},
		); err != nil {
			return err
		}
		return edit.SetJSON(path,
			edit.KV{Path: "security.auth.selectedType", Value: "gemini-api-key"},
			edit.KV{Path: "model.name", Value: model},
		)
	}
	// unroute puts back the base URL, key and auth type saved by route; ones
	// that did not exist are removed. The model is put back too when
	// restoreModel is set (leaving routing by changing auth); otherwise it is
	// left to the caller (leaving routing by picking a model).
	unrouteWith := func(restoreModel bool) error {
		if !routed() {
			return nil
		}
		m := stashLoad()
		saved := m[geminiStash["routed"]] != ""
		restoreEnv := func(name, key string) error {
			if v, ok := strings.CutPrefix(m[geminiStash[name]], "="); ok {
				return edit.SetEnvFile(envPath, edit.KV{Path: key, Value: v})
			}
			return edit.DelEnvFile(envPath, key)
		}
		if err := restoreEnv("base", "GOOGLE_GEMINI_BASE_URL"); err != nil {
			return err
		}
		if err := restoreEnv("key", "GEMINI_API_KEY"); err != nil {
			return err
		}
		if v, ok := strings.CutPrefix(m[geminiStash["auth"]], "="); ok {
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: v}); err != nil {
				return err
			}
		} else if saved {
			if err := delPruned(path, "security.auth.selectedType"); err != nil {
				return err
			}
		}
		if restoreModel {
			if v, ok := strings.CutPrefix(m[geminiStash["model"]], "="); ok {
				if err := edit.SetJSON(path, edit.KV{Path: "model.name", Value: v}); err != nil {
					return err
				}
			} else if saved {
				if err := delPruned(path, "model.name"); err != nil {
					return err
				}
			}
		}
		forget(geminiStashKeys()...)
		return nil
	}
	unroute := func() error { return unrouteWith(false) }
	setModel := func(v string) error {
		switch {
		case v == "":
			// Gemini CLI's own default model, and its own way in
			if err := unroute(); err != nil {
				return err
			}
			return delPruned(path, "model.name")
		case isDial(v):
			return route(v)
		}
		if err := unroute(); err != nil {
			return err
		}
		return edit.SetJSON(path, edit.KV{Path: "model.name", Value: v})
	}

	current := func() string {
		if routed() {
			return dialID
		}
		if base() != "" {
			return "custom"
		}
		switch auth() {
		case "gemini-api-key":
			return "api-key"
		case "vertex-ai":
			return "vertex"
		}
		return "google"
	}
	use := func(id string) error {
		if id == dialID {
			if !routed() {
				return fmt.Errorf("dial routes Gemini CLI through the local gateway once you pick a catalog model: run `dial gemini <provider/model>` (or choose one in the model picker) instead of setting auth to dial")
			}
			return nil
		}
		switch id {
		case "", "custom", "google", "vertex", "api-key":
		default:
			return fmt.Errorf("unknown provider %q", id)
		}
		// a catalog model only works through dial, so leaving dial by
		// changing auth also brings back the model the user had before
		if err := unrouteWith(true); err != nil {
			return err
		}
		switch id {
		case "":
			// back to the CLI's own first-run choice
			if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
				return err
			}
		case "custom":
			if current() != "custom" {
				return fmt.Errorf("custom means whatever GOOGLE_GEMINI_BASE_URL is already in %s; set it there", envPath)
			}
			return nil
		case "google":
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: "oauth-personal"}); err != nil {
				return err
			}
		case "vertex":
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: "vertex-ai"}); err != nil {
				return err
			}
		case "api-key":
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: "gemini-api-key"}); err != nil {
				return err
			}
			if k := dialKey(); k != "" && envKey() != k {
				if err := edit.SetEnvFile(envPath, edit.KV{Path: "GEMINI_API_KEY", Value: k}); err != nil {
					return err
				}
			}
		}
		return edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL")
	}

	return &Agent{
		ID: "gemini", Name: "Gemini CLI", Icon: "geminicli-color", Aliases: []string{"gemini-cli"},
		Bin: "gemini", Dir: dir, Path: path,
		Notice: func() string {
			if Running(`(^|/)gemini( |$)`) {
				return "Gemini CLI reads its settings at start-up — restart open gemini sessions to see this."
			}
			return ""
		},
		Fields: []Field{
			{
				Key: "provider", Label: "auth",
				Get: current,
				Set: use,
				Options: func(map[string]string) []Option {
					key := Option{Value: "api-key", Label: "API key", Icon: "gemini-color"}
					switch {
					case dialKey() != "":
						key.Note = "the Google key from dial's providers"
					case envKey() != "":
						key.Note = "GEMINI_API_KEY from ~/.gemini/.env"
					default:
						key.Note = "needs GEMINI_API_KEY — add Google Gemini in dial's providers"
					}
					out := []Option{
						{Value: "google", Label: "Google account", Icon: "gemini-color", Note: "OAuth sign-in"},
						key,
						{Value: "vertex", Label: "Vertex AI", Icon: "googlecloud-color", Note: "Vertex AI · $GOOGLE_CLOUD_PROJECT"},
					}
					switch current() {
					case "custom":
						out = append(out, Option{Value: "custom", Note: hostOf(base()) + " (from .env)"})
					case dialID:
						out = append(out, Option{Value: dialID, Label: "dial", Note: "local gateway · models from every provider"})
					}
					return out
				},
			},
			{
				Key: "model", Label: "model",
				Get: jsonGet(path, "model.name"),
				Set: setModel,
				Options: func(map[string]string) []Option {
					var ms []catalog.Model
					for _, m := range catalog.Provider("google") {
						if strings.HasPrefix(m.ID, "gemini-") {
							ms = append(ms, m)
						}
					}
					name := "Gemini CLI"
					if u := base(); u != "" && !routed() {
						name += " · " + hostOf(u)
					}
					return append(group(name, options(ms, "")), viaDial("")...)
				},
			},
		},
	}
}

// geminiStash names the stash entries holding Gemini CLI's pre-dial
// settings. Values are stored as "=" + value so an empty-but-present
// setting survives; a missing entry means the setting was absent.
var geminiStash = map[string]string{
	"routed": "gemini.routed",
	"base":   "gemini.base_url",
	"key":    "gemini.api_key",
	"auth":   "gemini.auth",
	"model":  "gemini.model",
}

func geminiStashKeys() []string {
	out := make([]string, 0, len(geminiStash))
	for _, k := range geminiStash {
		out = append(out, k)
	}
	return out
}

// delPruned deletes a JSON key path, then any parent objects it leaves empty.
func delPruned(path, key string) error {
	if err := edit.DelJSON(path, key); err != nil {
		return err
	}
	for {
		i := strings.LastIndex(key, ".")
		if i < 0 {
			return nil
		}
		key = key[:i]
		v, ok := edit.GetJSON(path, key)
		if !ok || strings.Join(strings.Fields(v), "") != "{}" {
			return nil
		}
		if err := edit.DelJSON(path, key); err != nil {
			return err
		}
	}
}
