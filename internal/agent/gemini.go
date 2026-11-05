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
// gateway: GOOGLE_GEMINI_BASE_URL and GEMINI_API_KEY go into .env, the auth
// type becomes gemini-api-key, and the values they replaced are stashed so
// picking a native model or Default puts them back.

// geminiAPIKey is the selectedType for Gemini API key auth.
const geminiAPIKey = "gemini-api-key"

// Stash keys for what the CLI's config said before dial routed it.
const (
	stashGeminiAuth = "gemini.auth"
	stashGeminiKey  = "gemini.key"
	stashGeminiBase = "gemini.base"
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
	// routed: the CLI's requests already go through the gateway.
	routed := func() bool { return base() == gateway.URL() && auth() == geminiAPIKey }

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
		case "vertex-ai":
			return "vertex"
		}
		return "google"
	}

	// route points the CLI at the gateway for a catalog model. The values
	// it replaces are stashed only the first time, while not yet routed.
	route := func(ref string) error {
		if !routed() {
			stash(map[string]string{stashGeminiAuth: auth(), stashGeminiKey: envKey(), stashGeminiBase: base()})
		}
		if err := edit.SetEnvFile(envPath,
			edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: gateway.URL()},
			edit.KV{Path: "GEMINI_API_KEY", Value: gateway.Token},
		); err != nil {
			return err
		}
		return edit.SetJSON(path,
			edit.KV{Path: "security.auth.selectedType", Value: geminiAPIKey},
			edit.KV{Path: "model.name", Value: ref},
		)
	}
	// unroute puts back auth type, key and base URL as they were before
	// routing. Anything that was absent is removed.
	unroute := func() error {
		if v := unstash(stashGeminiAuth); v != "" {
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: v}); err != nil {
				return err
			}
		} else if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
			return err
		}
		if v := unstash(stashGeminiKey); v != "" {
			if err := edit.SetEnvFile(envPath, edit.KV{Path: "GEMINI_API_KEY", Value: v}); err != nil {
				return err
			}
		} else if err := edit.DelEnvFile(envPath, "GEMINI_API_KEY"); err != nil {
			return err
		}
		if v := unstash(stashGeminiBase); v != "" {
			return edit.SetEnvFile(envPath, edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: v})
		}
		return edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL")
	}

	// providerOf names the vendor (or account) behind a routed model.
	providerOf := func() string {
		if !routed() {
			return ""
		}
		p, _, ok := provider.Resolve(model())
		if !ok {
			return ""
		}
		if p.Account != nil {
			return p.Account.User
		}
		return p.Name
	}

	setModel := func(v string) error {
		switch {
		case v == "":
			// Default: back to the CLI's own auth, key and base URL
			if routed() {
				if err := unroute(); err != nil {
					return err
				}
			}
			return edit.DelJSON(path, "model.name")
		case strings.Contains(v, "/"):
			if !isDial(v) {
				return fmt.Errorf("%q is not a model dial knows; see the model list", v)
			}
			return route(v)
		}
		// a native model (gemini-…, or a CLI alias such as "auto")
		if routed() {
			if err := unroute(); err != nil {
				return err
			}
		}
		return edit.SetJSON(path, edit.KV{Path: "model.name", Value: v})
	}

	setAuth := func(id string) error {
		if id == dialID {
			if routed() {
				return nil
			}
			return fmt.Errorf("dial is reached by choosing a model from dial's catalog: dial gemini model <provider/model>")
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
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: geminiAPIKey}); err != nil {
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
				Set: setAuth,
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
						out = append(out, Option{Value: dialID, Label: "dial", Note: "through dial's gateway · pick a model to change it"})
					}
					if current() == "custom" {
						out = append(out, Option{Value: "custom", Note: hostOf(base()) + " (from .env)"})
					}
					return out
				},
			},
			{
				// read-only: follows the model, so it can't be set on its own
				Key: "provider", Label: "provider",
				Get: providerOf,
				// setting the value it already shows (as a profile does) is a no-op
				Set: func(v string) error {
					if v == "" || v == providerOf() {
						return nil
					}
					return fmt.Errorf("the provider follows the model: set dial gemini model to provider/model")
				},
				Options: func(map[string]string) []Option { return nil },
			},
		},
	}
}
