package agent

import (
	"errors"
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
//
// Routing: picking a catalog model points the CLI at the gateway. That means
// GOOGLE_GEMINI_BASE_URL (in .env) = the gateway, GEMINI_API_KEY = the gateway
// token, selectedType = gemini-api-key and model.name = the catalog model.
// Before the first switch the four original values are stashed; leaving the
// gateway puts exactly those back, and a key that was absent is removed.

// Gemini CLI's own sign-in types, as settings.json names them.
const (
	geminiOAuth    = "oauth-personal"
	geminiAPIKey   = "gemini-api-key"
	geminiVertexAI = "vertex-ai"
)

func gemini(home string) *Agent {
	dir := filepath.Join(home, ".gemini")
	path := filepath.Join(dir, "settings.json")
	envPath := filepath.Join(dir, ".env")
	auth := jsonGet(path, "security.auth.selectedType")
	model := jsonGet(path, "model.name")
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
	routed := func() bool { return base() == gateway.URL() }

	current := func() string {
		if routed() {
			return dialID
		}
		if base() != "" {
			return "custom"
		}
		switch auth() {
		case geminiAPIKey:
			return "api-key"
		case geminiVertexAI:
			return "vertex"
		}
		return "google"
	}

	// setEnv writes a .env key, or removes it when the value is empty.
	setEnv := func(key, v string) error {
		if v == "" {
			return edit.DelEnvFile(envPath, key)
		}
		return edit.SetEnvFile(envPath, edit.KV{Path: key, Value: v})
	}

	// route points the CLI at the gateway for model m. The originals are
	// stashed only on the first switch, so moving between catalog models
	// never overwrites them.
	route := func(m string) error {
		if !routed() {
			stash(map[string]string{
				"gemini.base_url": base(),
				"gemini.api_key":  envKey(),
				"gemini.auth":     auth(),
				"gemini.model":    model(),
			})
		}
		if err := edit.SetEnvFile(envPath,
			edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: gateway.URL()},
			edit.KV{Path: "GEMINI_API_KEY", Value: gateway.Token},
		); err != nil {
			return err
		}
		return edit.SetJSON(path,
			edit.KV{Path: "security.auth.selectedType", Value: geminiAPIKey},
			edit.KV{Path: "model.name", Value: m},
		)
	}

	// unroute restores the stashed originals. Anything that was absent
	// before routing (an empty stash entry) is removed again.
	unroute := func() error {
		if err := setEnv("GOOGLE_GEMINI_BASE_URL", unstash("gemini.base_url")); err != nil {
			return err
		}
		if err := setEnv("GEMINI_API_KEY", unstash("gemini.api_key")); err != nil {
			return err
		}
		if t := unstash("gemini.auth"); t != "" {
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: t}); err != nil {
				return err
			}
		} else if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
			return err
		}
		if m := unstash("gemini.model"); m != "" {
			return edit.SetJSON(path, edit.KV{Path: "model.name", Value: m})
		}
		return edit.DelJSON(path, "model.name")
	}

	// useAuth picks one of Gemini CLI's own sign-in types.
	useAuth := func(id string) error {
		if id == "" {
			// back to the CLI's own first-run choice
			return edit.DelJSON(path, "security.auth.selectedType")
		}
		switch id {
		case "google":
			return edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: geminiOAuth})
		case "vertex":
			return edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: geminiVertexAI})
		case "api-key":
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: geminiAPIKey}); err != nil {
				return err
			}
			if k := dialKey(); k != "" && envKey() != k {
				return edit.SetEnvFile(envPath, edit.KV{Path: "GEMINI_API_KEY", Value: k})
			}
		}
		return nil
	}

	use := func(id string) error {
		switch id {
		case dialID:
			if routed() {
				return nil
			}
			return errors.New("dial is the local gateway; to use it, pick a model from dial's catalog " +
				"(e.g. `dial gemini <provider/model>`) and Gemini CLI is routed through it automatically")
		case "custom":
			if current() != "custom" {
				return fmt.Errorf("custom means whatever GOOGLE_GEMINI_BASE_URL is already in %s; set it there", envPath)
			}
			return nil
		case "", "google", "vertex", "api-key":
		default:
			return fmt.Errorf("unknown provider %q", id)
		}
		// leaving the gateway first puts the originals back, then the chosen auth applies
		wasRouted := routed()
		if wasRouted {
			if err := unroute(); err != nil {
				return err
			}
		}
		if err := useAuth(id); err != nil {
			return err
		}
		if wasRouted {
			return nil
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
		// Field order matches the original: auth first (Fields[0] is what
		// `dial gemini default` and the GUI's provider lookup read), model second.
		// Model logic is reached through Field("model") / fieldForValue, not by index.
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
						{Value: dialID, Label: dialID, Note: "local gateway · models from every provider (pick a catalog model)"},
					}
					if current() == "custom" {
						out = append(out, Option{Value: "custom", Note: hostOf(base()) + " (from .env)"})
					}
					return out
				},
			},
			{
				Key: "model", Label: "model",
				Get: model,
				Set: func(v string) error {
					switch {
					case v == "":
						if routed() {
							return unroute()
						}
						return edit.DelJSON(path, "model.name")
					case isDial(v):
						return route(v)
					}
					if routed() {
						if err := unroute(); err != nil {
							return err
						}
					}
					return edit.SetJSON(path, edit.KV{Path: "model.name", Value: v})
				},
				Options: func(map[string]string) []Option {
					var ms []catalog.Model
					for _, m := range catalog.Provider("google") {
						if strings.HasPrefix(m.ID, "gemini-") {
							ms = append(ms, m)
						}
					}
					return append(group("Google", options(ms, "")), viaDial("")...)
				},
			},
		},
	}
}
