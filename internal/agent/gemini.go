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
// Picking a catalog model routes the CLI through the gateway: the endpoint
// and key in .env, the auth type and model in settings.json. What those
// settings said before is stashed so that choosing a native model or the
// default puts them back exactly as they were.

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

	// route points the CLI at the gateway with a catalog model. The first
	// time, what the CLI had is stashed; while already routed the stash is
	// left alone so it keeps the original values.
	route := func(model string) error {
		if !routed() {
			stash(map[string]string{
				"gemini.base_url": base(),
				"gemini.api_key":  envKey(),
				"gemini.auth":     auth(),
				"gemini.model":    modelName(),
			})
		}
		if err := edit.SetEnvFile(envPath,
			edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: gateway.URL()},
			edit.KV{Path: "GEMINI_API_KEY", Value: gateway.Token}); err != nil {
			return err
		}
		return edit.SetJSON(path,
			edit.KV{Path: "security.auth.selectedType", Value: "gemini-api-key"},
			edit.KV{Path: "model.name", Value: model})
	}

	// unroute puts back each stashed value; a value that was absent before
	// routing is removed again.
	unroute := func() error {
		var envSet []edit.KV
		var envDel []string
		for _, kv := range []struct{ key, stashKey string }{
			{"GOOGLE_GEMINI_BASE_URL", "gemini.base_url"},
			{"GEMINI_API_KEY", "gemini.api_key"},
		} {
			if v := unstash(kv.stashKey); v != "" {
				envSet = append(envSet, edit.KV{Path: kv.key, Value: v})
			} else {
				envDel = append(envDel, kv.key)
			}
		}
		var jsonSetKV []edit.KV
		var jsonDel []string
		for _, kv := range []struct{ key, stashKey string }{
			{"security.auth.selectedType", "gemini.auth"},
			{"model.name", "gemini.model"},
		} {
			if v := unstash(kv.stashKey); v != "" {
				jsonSetKV = append(jsonSetKV, edit.KV{Path: kv.key, Value: v})
			} else {
				jsonDel = append(jsonDel, kv.key)
			}
		}
		if len(envDel) > 0 {
			if err := edit.DelEnvFile(envPath, envDel...); err != nil {
				return err
			}
		}
		if len(envSet) > 0 {
			if err := edit.SetEnvFile(envPath, envSet...); err != nil {
				return err
			}
		}
		if len(jsonDel) > 0 {
			if err := edit.DelJSON(path, jsonDel...); err != nil {
				return err
			}
		}
		if len(jsonSetKV) > 0 {
			if err := edit.SetJSON(path, jsonSetKV...); err != nil {
				return err
			}
		}
		return nil
	}

	setAuth := func(id string) error {
		switch id {
		case "":
			if routed() {
				return unroute()
			}
			// back to the CLI's own first-run choice
			if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
				return err
			}
			return edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL")
		case dialID:
			if routed() {
				return nil
			}
			return fmt.Errorf("Gemini CLI is not routed through dial yet; choose a dial model for it instead (dial gemini <provider/model>, or from the model list)")
		case "google", "vertex", "api-key", "custom":
		default:
			return fmt.Errorf("unknown provider %q", id)
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
		case "custom":
			if base() == "" {
				return fmt.Errorf("custom means whatever GOOGLE_GEMINI_BASE_URL is already in %s; set it there", envPath)
			}
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
						out = append(out, Option{Value: dialID, Label: "dial", Note: "local gateway · models of every vendor"})
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
					out := group("Gemini CLI", options(ms, ""))
					return append(out, viaDial("")...)
				},
			},
		},
	}
}
