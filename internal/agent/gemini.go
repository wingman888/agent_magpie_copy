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
// Any catalog model works too: the gateway serves the Gemini API, so dial
// points GOOGLE_GEMINI_BASE_URL at it, uses the gateway token as the API
// key and names the catalog model in model.name.

func gemini(home string) *Agent {
	dir := filepath.Join(home, ".gemini")
	path := filepath.Join(dir, "settings.json")
	envPath := filepath.Join(dir, ".env")
	auth := jsonGet(path, "security.auth.selectedType")
	model := jsonGet(path, "model.name")
	base := func() string { v, _ := edit.GetEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL"); return v }
	envKey := func() string { v, _ := edit.GetEnvFile(envPath, "GEMINI_API_KEY"); return v }
	routed := func() bool { return base() == gateway.URL() }
	dialKey := func() string {
		for _, id := range []string{"google", "gemini"} {
			if p, err := provider.Find(id); err == nil && p.Key != "" {
				return p.Key
			}
		}
		return ""
	}
	// unroute puts back what routing through the gateway replaced
	unroute := func() error {
		if !routed() {
			return nil
		}
		if err := edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL", "GEMINI_API_KEY"); err != nil {
			return err
		}
		var env []edit.KV
		if u := unstash("gemini.base_url"); u != "" {
			env = append(env, edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: u})
		}
		if k := unstash("gemini.api_key"); k != "" {
			env = append(env, edit.KV{Path: "GEMINI_API_KEY", Value: k})
		}
		if len(env) > 0 {
			if err := edit.SetEnvFile(envPath, env...); err != nil {
				return err
			}
		}
		if a := unstash("gemini.auth"); a != "" {
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: a}); err != nil {
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
	setModel := func(v string) error {
		if v == "" {
			if routed() {
				forget("gemini.base_url", "gemini.api_key", "gemini.auth", "gemini.model")
				if err := edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL", "GEMINI_API_KEY"); err != nil {
					return err
				}
				return edit.DelJSON(path, "security.auth.selectedType", "model.name")
			}
			return edit.DelJSON(path, "model.name")
		}
		if isDial(v) {
			if !routed() {
				stash(map[string]string{"gemini.base_url": base(), "gemini.api_key": envKey(), "gemini.auth": auth(), "gemini.model": model()})
			}
			if err := edit.SetEnvFile(envPath, edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: gateway.URL()}, edit.KV{Path: "GEMINI_API_KEY", Value: gateway.Token}); err != nil {
				return err
			}
			return edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: "gemini-api-key"}, edit.KV{Path: "model.name", Value: v})
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
			if routed() {
				return nil
			}
			return fmt.Errorf("pick a model via dial instead; that routes Gemini CLI through the gateway")
		}
		if err := unroute(); err != nil {
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
					case dialID:
						out = append(out, Option{Value: dialID, Label: "dial", Note: "the local gateway · every provider's models"})
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
					own := group("Gemini CLI", options(ms, ""))
					return append(own, viaDial("")...)
				},
			},
		},
	}
}
