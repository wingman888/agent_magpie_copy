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
// A catalog model routes Gemini CLI through the gateway: the .env points
// GOOGLE_GEMINI_BASE_URL at dial and GEMINI_API_KEY at its token, the auth
// type becomes gemini-api-key and model.name names the catalog model. What
// the user had in those four places is stashed on the first route and put
// back when they leave the gateway.

// stashed keys for the four settings a route overwrites.
const (
	gemBaseKey = "gemini.base_url"
	gemAPIKey  = "gemini.api_key"
	gemAuthKey = "gemini.auth"
	gemModel   = "gemini.model"
)

// geminiPutEnv sets a dotenv key, or removes it when the value is empty.
func geminiPutEnv(path, key, v string) error {
	if v == "" {
		return edit.DelEnvFile(path, key)
	}
	return edit.SetEnvFile(path, edit.KV{Path: key, Value: v})
}

// geminiPutJSON sets a settings key, or removes it when the value is empty.
func geminiPutJSON(path, key, v string) error {
	if v == "" {
		return edit.DelJSON(path, key)
	}
	return edit.SetJSON(path, edit.KV{Path: key, Value: v})
}

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
		case "gemini-api-key":
			return "api-key"
		case "vertex-ai":
			return "vertex"
		}
		return "google"
	}

	// route points Gemini CLI at the gateway for catalog model v. The
	// original values are stashed only on the first route, so switching
	// between catalog models never overwrites them.
	route := func(v string) error {
		if !routed() {
			stash(map[string]string{
				gemBaseKey: base(),
				gemAPIKey:  envKey(),
				gemAuthKey: auth(),
				gemModel:   model(),
			})
		}
		if err := edit.SetEnvFile(envPath,
			edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: gateway.URL()},
			edit.KV{Path: "GEMINI_API_KEY", Value: gateway.Token},
		); err != nil {
			return err
		}
		return edit.SetJSON(path,
			edit.KV{Path: "security.auth.selectedType", Value: "gemini-api-key"},
			edit.KV{Path: "model.name", Value: v},
		)
	}

	// unroute puts back exactly what was stashed; a value that was absent
	// before is removed again.
	unroute := func() error {
		if err := geminiPutEnv(envPath, "GOOGLE_GEMINI_BASE_URL", unstash(gemBaseKey)); err != nil {
			return err
		}
		if err := geminiPutEnv(envPath, "GEMINI_API_KEY", unstash(gemAPIKey)); err != nil {
			return err
		}
		if err := geminiPutJSON(path, "security.auth.selectedType", unstash(gemAuthKey)); err != nil {
			return err
		}
		return geminiPutJSON(path, "model.name", unstash(gemModel))
	}

	use := func(id string) error {
		if id == dialID {
			if !routed() {
				return fmt.Errorf("dial is the gateway route, not a sign-in of its own; " +
					"pick a dial model to switch Gemini CLI to it, e.g. `dial gemini model <provider/model>` " +
					"(`dial gemini model` lists them)")
			}
			return nil
		}
		switch id {
		case "", "custom", "google", "vertex", "api-key":
		default:
			return fmt.Errorf("unknown provider %q", id)
		}
		// restored means unroute put the user's own base URL back, which
		// must survive the removal below.
		restored := false
		if routed() {
			// leaving the gateway: every value goes back to what it was
			if err := unroute(); err != nil {
				return err
			}
			if id == "" {
				return nil
			}
			restored = true
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
		default:
			return fmt.Errorf("unknown provider %q", id)
		}
		if restored {
			return nil
		}
		return edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL")
	}

	setModel := func(v string) error {
		if v == "" {
			if routed() {
				return unroute()
			}
			return edit.DelJSON(path, "model.name")
		}
		if isDial(v) {
			return route(v)
		}
		if routed() {
			if err := unroute(); err != nil {
				return err
			}
		}
		return edit.SetJSON(path, edit.KV{Path: "model.name", Value: v})
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
					dialNote := "local gateway · models from every provider in dial"
					if !routed() {
						dialNote += " (pick a dial model under model to switch)"
					}
					out := []Option{
						{Value: "google", Label: "Google account", Icon: "gemini-color", Note: "OAuth sign-in"},
						key,
						{Value: "vertex", Label: "Vertex AI", Icon: "googlecloud-color", Note: "Vertex AI · $GOOGLE_CLOUD_PROJECT"},
						{Value: dialID, Label: "dial", Icon: "gemini-color", Note: dialNote},
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
				Set: setModel,
				Options: func(map[string]string) []Option {
					var ms []catalog.Model
					for _, m := range catalog.Provider("google") {
						if strings.HasPrefix(m.ID, "gemini-") {
							ms = append(ms, m)
						}
					}
					out := group("Gemini CLI", options(ms, ""))
					return append(out, viaDial("")...)
				},
			},
		},
	}
}
