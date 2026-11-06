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

// Gemini CLI only speaks Google's own API, so its "auth" is how it
// authenticates: Google sign-in, a Gemini API key, or Vertex AI. The choice
// lives in settings.json (security.auth.selectedType). A Google provider
// added in dial lends its key through ~/.gemini/.env, which the CLI loads.
//
// A catalog model ("provider/model") routes the CLI through the gateway: the
// key and base URL in .env point at dial, and the API-key auth type is used.
// What the CLI had before is stashed, so picking a native model (or Default)
// puts it back.

// stash keys for the values routing replaces.
const (
	gemSelected = "gemini.selected_type"
	gemKey      = "gemini.api_key"
	gemBase     = "gemini.base_url"
	gemModel    = "gemini.model"
)

func gemini(home string) *Agent {
	dir := filepath.Join(home, ".gemini")
	path := filepath.Join(dir, "settings.json")
	envPath := filepath.Join(dir, ".env")
	selected := jsonGet(path, "security.auth.selectedType")
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

	// routed: the CLI is pointed at the gateway with API-key auth.
	routed := func() bool {
		return base() == gateway.URL() && selected() == "gemini-api-key"
	}

	current := func() string {
		if routed() {
			return dialID
		}
		if base() != "" {
			return "custom"
		}
		switch selected() {
		case "gemini-api-key":
			return "api-key"
		case "vertex-ai":
			return "vertex"
		}
		return "google"
	}

	// unroute puts back the auth type, key and base URL the CLI had before
	// dial routed it. Values it did not have are removed.
	unroute := func() error {
		sel, key, b := unstash(gemSelected), unstash(gemKey), unstash(gemBase)
		unstash(gemModel)
		if sel != "" {
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: sel}); err != nil {
				return err
			}
		} else if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
			return err
		}
		if key != "" {
			if err := edit.SetEnvFile(envPath, edit.KV{Path: "GEMINI_API_KEY", Value: key}); err != nil {
				return err
			}
		} else if err := edit.DelEnvFile(envPath, "GEMINI_API_KEY"); err != nil {
			return err
		}
		if b != "" {
			return edit.SetEnvFile(envPath, edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: b})
		}
		return edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL")
	}

	// route points the CLI at the gateway and names the catalog model ref.
	route := func(ref string) error {
		if !routed() {
			stash(map[string]string{
				gemSelected: selected(),
				gemKey:      envKey(),
				gemBase:     base(),
				gemModel:    model(),
			})
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

	// setModel handles the model field: "" is the CLI's default, a
	// "provider/model" ref goes through dial, anything else is native.
	setModel := func(v string) error {
		switch {
		case v == "":
			if routed() {
				if err := unroute(); err != nil {
					return err
				}
			}
			return edit.DelJSON(path, "model.name")
		case strings.Contains(v, "/"):
			if !isDial(v) {
				return fmt.Errorf("unknown model %q — dial's models lists the catalog", v)
			}
			return route(v)
		}
		if routed() {
			if err := unroute(); err != nil {
				return err
			}
		}
		return edit.SetJSON(path, edit.KV{Path: "model.name", Value: v})
	}

	useAuth := func(id string) error {
		switch id {
		case dialID:
			if routed() {
				return nil
			}
			return fmt.Errorf("dial is not an auth choice: pick a dial model (dial gemini <provider>/<model>) to route Gemini CLI through the gateway")
		case "custom":
			if current() != "custom" {
				return fmt.Errorf("custom means whatever GOOGLE_GEMINI_BASE_URL is already in %s; set it there", envPath)
			}
			return nil
		}
		if routed() {
			if err := unroute(); err != nil {
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
				Key: "model", Label: "model",
				Get: model,
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
				Key: "auth", Label: "auth",
				Get: current,
				Set: useAuth,
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
						out = append(out, Option{Value: dialID, Note: "through dial's gateway · pick another model to leave it"})
					}
					return out
				},
			},
			{
				Key: "provider", Label: "provider",
				Get: func() string {
					if !routed() {
						return ""
					}
					if p, _, ok := strings.Cut(model(), "/"); ok {
						return p
					}
					return ""
				},
				Set: func(v string) error {
					if v == "" {
						return setModel("")
					}
					p, err := provider.Find(v)
					if err != nil {
						return err
					}
					if routed() {
						if cur, _, ok := strings.Cut(model(), "/"); ok && cur == p.ID {
							return nil
						}
					}
					for _, e := range provider.Catalog() {
						if e.Provider.ID == p.ID {
							return route(e.ID)
						}
					}
					return fmt.Errorf("%s has no models in dial's catalog — pick some in dial's providers", p.Name)
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
							Note: dialNote(e.Provider), Icon: e.Provider.Icon})
					}
					return out
				},
			},
		},
	}
}
