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
//
// Routing through dial is reached by picking a catalog model ("provider/model")
// in the model field. The gateway then speaks the Gemini API to the CLI, so
// the CLI's base URL and key point at it. What the CLI had before is stashed
// so that picking a native model, or Default, puts it back.

// Stash keys for the values routing overwrites.
const (
	gemSelected = "gemini.selectedType"
	gemKey      = "gemini.key"
	gemBase     = "gemini.base"
	gemModel    = "gemini.model"
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

	// routed: the CLI already points at this gateway with the API-key method.
	routed := func() bool {
		return strings.TrimRight(base(), "/") == gateway.URL() && auth() == "gemini-api-key"
	}

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

	// route sends the CLI through the gateway for a catalog model. The values
	// it replaces are stashed the first time only; routing again must not
	// stash the gateway's own values.
	route := func(ref string) error {
		if _, _, ok := provider.Resolve(ref); !ok {
			return fmt.Errorf("no provider serves %q — dial providers lists them", ref)
		}
		if !routed() {
			stash(map[string]string{
				gemSelected: auth(), gemKey: envKey(), gemBase: base(), gemModel: model(),
			})
		}
		if err := edit.SetEnvFile(envPath,
			edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: gateway.URL()},
			edit.KV{Path: "GEMINI_API_KEY", Value: gateway.Token},
		); err != nil {
			return err
		}
		if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: "gemini-api-key"}); err != nil {
			return err
		}
		return edit.SetJSON(path, edit.KV{Path: "model.name", Value: ref})
	}

	// unroute puts back what routing replaced. A value that was absent before
	// is removed. The stash is cleared only once the files are written.
	unroute := func() error {
		m := stashLoad()
		if v := m[gemSelected]; v != "" {
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: v}); err != nil {
				return err
			}
		} else if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
			return err
		}
		if v := m[gemKey]; v != "" {
			if err := edit.SetEnvFile(envPath, edit.KV{Path: "GEMINI_API_KEY", Value: v}); err != nil {
				return err
			}
		} else if err := edit.DelEnvFile(envPath, "GEMINI_API_KEY"); err != nil {
			return err
		}
		if v := m[gemBase]; v != "" {
			if err := edit.SetEnvFile(envPath, edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: v}); err != nil {
				return err
			}
		} else if err := edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL"); err != nil {
			return err
		}
		if v := m[gemModel]; v != "" {
			if err := edit.SetJSON(path, edit.KV{Path: "model.name", Value: v}); err != nil {
				return err
			}
		} else if err := edit.DelJSON(path, "model.name"); err != nil {
			return err
		}
		forget(gemSelected, gemKey, gemBase, gemModel)
		return nil
	}

	use := func(id string) error {
		switch id {
		case "dial":
			if routed() {
				return nil
			}
			return fmt.Errorf("dial is reached by picking a provider/model in the model field, not by setting auth to dial")
		case "custom":
			if current() != "custom" {
				return fmt.Errorf("custom means whatever GOOGLE_GEMINI_BASE_URL is already in %s; set it there", envPath)
			}
			return nil
		case "":
			if routed() {
				// back to what the CLI had before dial routed it
				return unroute()
			}
			// the CLI's own first-run choice
			if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
				return err
			}
			return edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL")
		}
		if routed() {
			if err := unroute(); err != nil {
				return err
			}
		}
		switch id {
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
		default:
			return fmt.Errorf("unknown provider %q", id)
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
					case "dial":
						out = append(out, Option{Value: "dial", Note: "through dial's gateway · pick a model to change"})
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
							if err := unroute(); err != nil {
								return err
							}
						}
						return edit.DelJSON(path, "model.name")
					case strings.Contains(v, "/"):
						return route(v)
					default:
						// a native model: back to the CLI's own endpoint and key
						if routed() {
							if err := unroute(); err != nil {
								return err
							}
						}
						return edit.SetJSON(path, edit.KV{Path: "model.name", Value: v})
					}
				},
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
		},
	}
}
