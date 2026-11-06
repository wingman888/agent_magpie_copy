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
// Picking a catalog model routes the CLI through the gateway: the base URL
// and key in .env point at dial, selectedType becomes gemini-api-key and
// model.name names the catalog model. Whatever those four settings held
// before is stashed on the first routing and put back when the CLI leaves
// the gateway, so Google sign-in, a personal key or a custom base URL survive.

// geminiStashKeys are the four settings routing overwrites, by stash key.
const (
	geminiStashBase  = "gemini.base_url"
	geminiStashKey   = "gemini.api_key"
	geminiStashAuth  = "gemini.auth"
	geminiStashModel = "gemini.model"
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
		case "gemini-api-key":
			return "api-key"
		case "vertex-ai":
			return "vertex"
		}
		return "google"
	}

	// route points the CLI at the gateway for catalog model v. The original
	// values are stashed only on the first routing; a later catalog model
	// leaves them alone.
	route := func(v string) error {
		if !routed() {
			stash(map[string]string{
				geminiStashBase:  base(),
				geminiStashKey:   envKey(),
				geminiStashAuth:  auth(),
				geminiStashModel: model(),
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

	// unroute puts back the stashed values; a value that was never set
	// (nothing stashed) is removed again.
	unroute := func() error {
		b, k, a, m := unstash(geminiStashBase), unstash(geminiStashKey), unstash(geminiStashAuth), unstash(geminiStashModel)
		if b != "" {
			if err := edit.SetEnvFile(envPath, edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: b}); err != nil {
				return err
			}
		} else if err := edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL"); err != nil {
			return err
		}
		if k != "" {
			if err := edit.SetEnvFile(envPath, edit.KV{Path: "GEMINI_API_KEY", Value: k}); err != nil {
				return err
			}
		} else if err := edit.DelEnvFile(envPath, "GEMINI_API_KEY"); err != nil {
			return err
		}
		if a != "" {
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: a}); err != nil {
				return err
			}
		} else if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
			return err
		}
		if m != "" {
			return edit.SetJSON(path, edit.KV{Path: "model.name", Value: m})
		}
		return edit.DelJSON(path, "model.name")
	}

	use := func(id string) error {
		switch id {
		case dialID:
			if routed() {
				return nil
			}
			return fmt.Errorf("dial is not set up for Gemini CLI yet; choose a catalog model (dial gemini model <provider/model>) to route through it")
		case "custom":
			if current() != "custom" {
				return fmt.Errorf("custom means whatever GOOGLE_GEMINI_BASE_URL is already in %s; set it there", envPath)
			}
			return nil
		case "":
			// back to the CLI's own first-run choice
			if routed() {
				return unroute()
			}
			if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
				return err
			}
			return edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL")
		case "google", "vertex", "api-key":
		default:
			return fmt.Errorf("unknown provider %q", id)
		}

		wasRouted := routed()
		if wasRouted {
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
			// Not routed: the key from dial's providers replaces a different
			// GEMINI_API_KEY, as it always has. Coming back from routing, a key
			// restored from before routing is kept; only a missing one is filled.
			if k := dialKey(); k != "" && envKey() != k && (!wasRouted || envKey() == "") {
				if err := edit.SetEnvFile(envPath, edit.KV{Path: "GEMINI_API_KEY", Value: k}); err != nil {
					return err
				}
			}
		}
		if wasRouted {
			return nil // unroute already restored the base URL
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
			// a Google model while routed: give back the CLI's own settings
			// exactly as they were before routing, model.name included.
			return unroute()
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
					out := []Option{
						{Value: "google", Label: "Google account", Icon: "gemini-color", Note: "OAuth sign-in"},
						key,
						{Value: "vertex", Label: "Vertex AI", Icon: "googlecloud-color", Note: "Vertex AI · $GOOGLE_CLOUD_PROJECT"},
						{Value: dialID, Label: "dial", Icon: "gemini-color", Note: "local gateway · models from every provider"},
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
					own := group("Google Gemini", options(ms, ""))
					return append(own, viaDial("")...)
				},
			},
		},
	}
}
