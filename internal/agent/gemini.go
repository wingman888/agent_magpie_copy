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
// Picking a dial catalog model routes the CLI through the gateway: the base
// URL, key, auth type and model are rewritten. What they were before is kept
// in dial's stash (keys "gemini.*"), so leaving the route restores them, and
// a value that was absent is restored as absent.

// geminiDial is the auth value shown while the CLI is routed to the gateway.
const geminiDial = "dial"

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
			return geminiDial
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

	// saveOriginals stashes what the CLI was configured with before the
	// first route. Absent values are stored as "" (which the stash drops),
	// so restoring them deletes the key again.
	saveOriginals := func() {
		stash(map[string]string{
			"gemini.base_url": base(),
			"gemini.api_key":  envKey(),
			"gemini.auth":     auth(),
			"gemini.model":    modelName(),
		})
	}

	// restore undoes the route, putting back the stashed values exactly.
	restore := func() error {
		if !routed() {
			return nil
		}
		baseURL, key := unstash("gemini.base_url"), unstash("gemini.api_key")
		authType, model := unstash("gemini.auth"), unstash("gemini.model")

		var envSet []edit.KV
		var envDel []string
		if baseURL != "" {
			envSet = append(envSet, edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: baseURL})
		} else {
			envDel = append(envDel, "GOOGLE_GEMINI_BASE_URL")
		}
		if key != "" {
			envSet = append(envSet, edit.KV{Path: "GEMINI_API_KEY", Value: key})
		} else {
			envDel = append(envDel, "GEMINI_API_KEY")
		}
		if err := edit.DelEnvFile(envPath, envDel...); err != nil {
			return err
		}
		if len(envSet) > 0 {
			if err := edit.SetEnvFile(envPath, envSet...); err != nil {
				return err
			}
		}

		var jsonSets []edit.KV
		var jsonDel []string
		if authType != "" {
			jsonSets = append(jsonSets, edit.KV{Path: "security.auth.selectedType", Value: authType})
		} else {
			jsonDel = append(jsonDel, "security.auth.selectedType")
		}
		if model != "" {
			jsonSets = append(jsonSets, edit.KV{Path: "model.name", Value: model})
		} else {
			jsonDel = append(jsonDel, "model.name")
		}
		if err := edit.DelJSON(path, jsonDel...); err != nil {
			return err
		}
		if len(jsonSets) > 0 {
			return edit.SetJSON(path, jsonSets...)
		}
		return nil
	}

	// route points the CLI at the gateway for catalog model v.
	route := func(v string) error {
		if !routed() {
			saveOriginals()
		}
		if err := edit.SetJSON(path,
			edit.KV{Path: "security.auth.selectedType", Value: "gemini-api-key"},
			edit.KV{Path: "model.name", Value: v},
		); err != nil {
			return err
		}
		return edit.SetEnvFile(envPath,
			edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: gateway.URL()},
			edit.KV{Path: "GEMINI_API_KEY", Value: gateway.Token},
		)
	}

	use := func(id string) error {
		if id == geminiDial {
			if routed() {
				return nil
			}
			return fmt.Errorf("dial is enabled by picking a dial model (e.g. `dial gemini model <provider/model>`), which routes Gemini CLI through the gateway")
		}
		switch id {
		case "", "custom", "google", "vertex", "api-key":
		default:
			return fmt.Errorf("unknown provider %q", id)
		}
		wasRouted := routed()
		if err := restore(); err != nil {
			return err
		}
		switch id {
		case "":
			if wasRouted {
				// the route is undone; the CLI's own settings are back
				return nil
			}
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
		}
		return edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL")
	}

	setModel := func(v string) error {
		if v == "" {
			if routed() {
				// default: leave the route and clear what dial put in
				return restore()
			}
			return edit.DelJSON(path, "model.name")
		}
		if isDial(v) {
			return route(v)
		}
		if err := restore(); err != nil {
			return err
		}
		return jsonSet(path, "model.name")(v)
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
				Key: "provider", Label: "auth", Alias: "auth",
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
					if current() == "custom" {
						out = append(out, Option{Value: "custom", Note: hostOf(base()) + " (from .env)"})
					}
					out = append(out, Option{Value: geminiDial, Note: "local gateway · models of every provider in dial's catalog (chosen under model)"})
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
