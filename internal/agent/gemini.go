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
// Routing through the gateway points GOOGLE_GEMINI_BASE_URL at dial, sets
// GEMINI_API_KEY to dial's token, selects gemini-api-key and names a catalog
// model. What was there before is stashed first, so leaving the gateway
// puts it back exactly.

// stash keys for the values routing overwrites
const (
	gemBase  = "gemini.base_url"
	gemKey   = "gemini.api_key"
	gemAuth  = "gemini.auth"
	gemModel = "gemini.model"
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

	// route points the CLI at the gateway and names the catalog model.
	// The original values are stashed only the first time: a second switch
	// between catalog models must not overwrite them.
	route := func(ref string) error {
		if !routed() {
			stash(map[string]string{gemBase: base(), gemKey: envKey(), gemAuth: auth(), gemModel: model()})
		}
		if err := edit.SetEnvFile(envPath,
			edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: gateway.URL()},
			edit.KV{Path: "GEMINI_API_KEY", Value: gateway.Token},
		); err != nil {
			return err
		}
		return edit.SetJSON(path,
			edit.KV{Path: "security.auth.selectedType", Value: "gemini-api-key"},
			edit.KV{Path: "model.name", Value: ref},
		)
	}

	// unroute puts back every value route stashed; a value that was absent
	// before routing is removed again.
	unroute := func() error {
		baseV, keyV := unstash(gemBase), unstash(gemKey)
		var envSet []edit.KV
		var envDel []string
		for _, kv := range []struct{ key, val string }{{"GOOGLE_GEMINI_BASE_URL", baseV}, {"GEMINI_API_KEY", keyV}} {
			if kv.val != "" {
				envSet = append(envSet, edit.KV{Path: kv.key, Value: kv.val})
			} else {
				envDel = append(envDel, kv.key)
			}
		}
		authV, modelV := unstash(gemAuth), unstash(gemModel)
		var jsonSet []edit.KV
		var jsonDel []string
		for _, kv := range []struct{ key, val string }{{"security.auth.selectedType", authV}, {"model.name", modelV}} {
			if kv.val != "" {
				jsonSet = append(jsonSet, edit.KV{Path: kv.key, Value: kv.val})
			} else {
				jsonDel = append(jsonDel, kv.key)
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
		if err := edit.DelJSON(path, jsonDel...); err != nil {
			return err
		}
		if len(jsonSet) > 0 {
			return edit.SetJSON(path, jsonSet...)
		}
		return nil
	}

	use := func(id string) error {
		if id == dialID {
			if routed() {
				return nil
			}
			return fmt.Errorf("dial is switched on by picking a catalog model instead: dial gemini model <provider/model>")
		}
		if id == "" {
			if routed() {
				return unroute()
			}
			// back to the CLI's own first-run choice
			if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
				return err
			}
			return edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL")
		}
		if routed() {
			// leaving the gateway: restore first, then apply the chosen auth
			if err := unroute(); err != nil {
				return err
			}
		}
		switch id {
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

	setModel := func(v string) error {
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
				Key: "provider", Label: "auth", Alias: []string{"auth"},
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
						{Value: dialID, Label: "dial", Note: "local gateway · models from every provider (pick one under model)"},
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
					return append(group("Google", options(ms, "")), viaDial("")...)
				},
			},
		},
	}
}
