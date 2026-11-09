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

// Gemini CLI only speaks Google's own API, so "auth" here is how it
// authenticates: Google sign-in, a Gemini API key, or Vertex AI. The choice
// lives in settings.json (security.auth.selectedType). A Google provider
// added in dial lends its key through ~/.gemini/.env, which the CLI loads.
//
// Picking a catalog model ("provider/model") routes the CLI through the
// gateway: the base URL and key in .env point at dial, and selectedType is
// gemini-api-key. The three values it replaces are stashed in dial's own
// config so that picking a native model or Default puts them back.

// geminiStash keys: the values Gemini CLI had before dial routed it. A key
// missing from the stash means the setting was absent, and restoring removes it.
const (
	geminiStashAuth = "gemini.auth"
	geminiStashKey  = "gemini.key"
	geminiStashBase = "gemini.base"
)

func gemini(home string) *Agent {
	dir := filepath.Join(home, ".gemini")
	path := filepath.Join(dir, "settings.json")
	envPath := filepath.Join(dir, ".env")
	auth := jsonGet(path, "security.auth.selectedType")
	modelName := jsonGet(path, "model.name")
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
	// routed: the CLI sends its requests to the gateway for a catalog model.
	routed := func() bool { return base() == gateway.URL() && auth() == "gemini-api-key" }

	current := func() string {
		if routed() {
			return "dial"
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

	// restore puts back the auth type, key and base URL from before routing.
	restore := func() error {
		m := stashLoad()
		if v := m[geminiStashAuth]; v != "" {
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: v}); err != nil {
				return err
			}
		} else if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
			return err
		}
		if v := m[geminiStashKey]; v != "" {
			if err := edit.SetEnvFile(envPath, edit.KV{Path: "GEMINI_API_KEY", Value: v}); err != nil {
				return err
			}
		} else if err := edit.DelEnvFile(envPath, "GEMINI_API_KEY"); err != nil {
			return err
		}
		if v := m[geminiStashBase]; v != "" {
			if err := edit.SetEnvFile(envPath, edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: v}); err != nil {
				return err
			}
		} else if err := edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL"); err != nil {
			return err
		}
		forget(geminiStashAuth, geminiStashKey, geminiStashBase)
		return nil
	}

	// route points Gemini CLI at the gateway for a catalog model reference.
	route := func(ref string) error {
		if !routed() {
			stash(map[string]string{geminiStashAuth: auth(), geminiStashKey: envKey(), geminiStashBase: base()})
		}
		if err := edit.SetJSON(path,
			edit.KV{Path: "security.auth.selectedType", Value: "gemini-api-key"},
			edit.KV{Path: "model.name", Value: ref},
		); err != nil {
			return err
		}
		return edit.SetEnvFile(envPath,
			edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: gateway.URL()},
			edit.KV{Path: "GEMINI_API_KEY", Value: gateway.Token},
		)
	}

	// setModel handles the model field: a catalog reference routes, a native
	// model or Default leaves routing first.
	setModel := func(v string) error {
		switch {
		case v == "":
			if routed() {
				if err := restore(); err != nil {
					return err
				}
			}
			return edit.DelJSON(path, "model.name")
		case strings.Contains(v, "/"):
			if !isDial(v) {
				return fmt.Errorf("%q is not a model dial offers — dial providers lists them", v)
			}
			return route(v)
		}
		if routed() {
			if err := restore(); err != nil {
				return err
			}
		}
		return edit.SetJSON(path, edit.KV{Path: "model.name", Value: v})
	}

	use := func(id string) error {
		switch id {
		case "dial":
			if routed() {
				return nil
			}
			return fmt.Errorf("dial is set by picking a catalog model (dial gemini model <provider>/<model>), not by hand")
		case "custom":
			if current() != "custom" {
				return fmt.Errorf("custom means whatever GOOGLE_GEMINI_BASE_URL is already in %s; set it there", envPath)
			}
			return nil
		case "", "google", "vertex", "api-key":
		default:
			return fmt.Errorf("unknown provider %q", id)
		}
		if routed() {
			if err := restore(); err != nil {
				return err
			}
		}
		switch id {
		case "":
			// back to the CLI's own first-run choice
			if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
				return err
			}
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
				Key: "auth", Label: "auth",
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
					if routed() {
						out = append(out, Option{Value: "dial", Note: "through the gateway, picked with a model"})
					}
					if current() == "custom" {
						out = append(out, Option{Value: "custom", Note: hostOf(base()) + " (from .env)"})
					}
					return out
				},
			},
			{
				Key: "model", Label: "model",
				Get: modelName,
				Set: setModel,
				Options: func(map[string]string) []Option {
					var ms []catalog.Model
					for _, m := range catalog.Provider("google") {
						if strings.HasPrefix(m.ID, "gemini-") {
							ms = append(ms, m)
						}
					}
					return append(options(ms, ""), viaDial("")...)
				},
			},
			{
				// the dial provider a catalog model comes from; picking one
				// keeps the model the user already chose from that provider.
				Key: "provider", Label: "provider",
				Get: func() string {
					if p, _, ok := strings.Cut(modelName(), "/"); ok {
						return p
					}
					return ""
				},
				Set: func(v string) error {
					if v == "" {
						return setModel("")
					}
					_, mid, ok := strings.Cut(modelName(), "/")
					if !ok || mid == "" {
						return fmt.Errorf("pick a model first: dial gemini model %s/<model>", v)
					}
					ref := v + "/" + mid
					if !isDial(ref) {
						return fmt.Errorf("%s has no model %q in dial", v, mid)
					}
					return route(ref)
				},
				Options: func(map[string]string) []Option {
					var out []Option
					seen := map[string]bool{}
					for _, e := range provider.Catalog() {
						if seen[e.Provider.ID] {
							continue
						}
						seen[e.Provider.ID] = true
						out = append(out, Option{Value: e.Provider.ID, Label: e.Provider.Name,
							Note: "models via dial", Icon: e.Provider.Icon})
					}
					return out
				},
			},
		},
	}
}
