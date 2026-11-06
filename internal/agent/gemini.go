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
// Picking a catalog model routes the CLI through the gateway: the .env gets
// the gateway URL and token, settings.json gets the gemini-api-key choice and
// the model. What the config said before is stashed first, so picking a
// Google model or the default again puts it back exactly.

// gemStash keys: the four settings a route overwrites.
const (
	gemStashBase  = "gemini.base_url"
	gemStashKey   = "gemini.api_key"
	gemStashAuth  = "gemini.auth"
	gemStashModel = "gemini.model"
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

	// route points the CLI at the gateway. The originals are stashed only on
	// the first route, so switching between catalog models never overwrites them.
	route := func(model string) error {
		if !routed() {
			stash(map[string]string{
				gemStashBase:  base(),
				gemStashKey:   envKey(),
				gemStashAuth:  auth(),
				gemStashModel: modelName(),
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
			edit.KV{Path: "model.name", Value: model},
		)
	}

	// unroute puts back the stashed originals; a value that was absent before
	// is removed rather than left behind.
	unroute := func() error {
		var envSet []edit.KV
		var envDel []string
		for _, kv := range []struct{ stashKey, env string }{
			{gemStashBase, "GOOGLE_GEMINI_BASE_URL"},
			{gemStashKey, "GEMINI_API_KEY"},
		} {
			if v := unstash(kv.stashKey); v != "" {
				envSet = append(envSet, edit.KV{Path: kv.env, Value: v})
			} else {
				envDel = append(envDel, kv.env)
			}
		}
		if err := edit.DelEnvFile(envPath, envDel...); err != nil {
			return err
		}
		if len(envSet) > 0 {
			if err := edit.SetEnvFile(envPath, envSet...); err != nil {
				return err
			}
		}
		if v := unstash(gemStashAuth); v != "" {
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: v}); err != nil {
				return err
			}
		} else if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
			return err
		}
		if v := unstash(gemStashModel); v != "" {
			return edit.SetJSON(path, edit.KV{Path: "model.name", Value: v})
		}
		return edit.DelJSON(path, "model.name")
	}

	use := func(id string) error {
		if id == dialID {
			if routed() {
				return nil
			}
			return errors.New("Gemini CLI isn't routed through dial yet — choose a dial model instead " +
				"(dial gemini model <provider/model>); that switches the auth to dial by itself")
		}
		if routed() {
			if err := unroute(); err != nil {
				return err
			}
			if id == "" {
				// the originals are back; "default" leaves them as they are
				return nil
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

	// setModel: a catalog model routes; a Google model or "default" undoes any route first.
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
					out := []Option{
						{Value: "google", Label: "Google account", Icon: "gemini-color", Note: "OAuth sign-in"},
						key,
						{Value: "vertex", Label: "Vertex AI", Icon: "googlecloud-color", Note: "Vertex AI · $GOOGLE_CLOUD_PROJECT"},
					}
					if routed() {
						out = append([]Option{{Value: dialID, Label: "dial", Note: "local gateway · models from every provider"}}, out...)
					} else if current() == "custom" {
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
