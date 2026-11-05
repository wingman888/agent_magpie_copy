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
// gateway: base URL and token go into .env, the key type into settings.json.
// What those keys held before is stashed in dial's own config, so picking a
// native model or the default puts it all back.

// Stash keys for the values routing overwrites. An absent value is stored
// as "" (which stash treats as no entry) and comes back as absent.
const (
	geminiStashType = "gemini.selectedType"
	geminiStashKey  = "gemini.key"
	geminiStashBase = "gemini.base"
	geminiStashOn   = "gemini.routed"
)

func gemini(home string) *Agent {
	dir := filepath.Join(home, ".gemini")
	path := filepath.Join(dir, "settings.json")
	envPath := filepath.Join(dir, ".env")
	auth := jsonGet(path, "security.auth.selectedType")
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

	// routed: the CLI is pointed at the gateway with the gateway's token.
	routed := func() bool {
		return strings.TrimRight(base(), "/") == gateway.URL() && auth() == "gemini-api-key"
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

	// route remembers what routing is about to overwrite. A route already in
	// place keeps the snapshot it was taken with.
	route := func() {
		if routed() {
			return
		}
		stash(map[string]string{geminiStashType: auth(), geminiStashKey: envKey(),
			geminiStashBase: base(), geminiStashOn: "1"})
	}

	// leave takes the CLI off the gateway, putting back the snapshot. A route
	// with no snapshot (set up by hand) only loses the gateway address and token.
	leave := func() error {
		if !routed() {
			if stashLoad()[geminiStashOn] != "" {
				forget(geminiStashType, geminiStashKey, geminiStashBase, geminiStashOn)
			}
			return nil
		}
		if unstash(geminiStashOn) == "" {
			if err := edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL"); err != nil {
				return err
			}
			if envKey() == gateway.Token {
				return edit.DelEnvFile(envPath, "GEMINI_API_KEY")
			}
			return nil
		}
		typ, key, b := unstash(geminiStashType), unstash(geminiStashKey), unstash(geminiStashBase)
		if typ != "" {
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: typ}); err != nil {
				return err
			}
		} else if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
			return err
		}
		if err := setOrDelEnv(envPath, "GEMINI_API_KEY", key); err != nil {
			return err
		}
		return setOrDelEnv(envPath, "GOOGLE_GEMINI_BASE_URL", b)
	}

	// setModel is the model field's Set, and what the provider field uses too.
	setModel := func(v string) error {
		switch {
		case v == "":
			if err := leave(); err != nil {
				return err
			}
			return edit.DelJSON(path, "model.name")
		case strings.Contains(v, "/"):
			route()
			if err := edit.SetEnvFile(envPath,
				edit.KV{Path: "GEMINI_API_KEY", Value: gateway.Token},
				edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: gateway.URL()},
			); err != nil {
				return err
			}
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: "gemini-api-key"}); err != nil {
				return err
			}
			return edit.SetJSON(path, edit.KV{Path: "model.name", Value: v})
		}
		if err := leave(); err != nil {
			return err
		}
		return edit.SetJSON(path, edit.KV{Path: "model.name", Value: v})
	}

	use := func(id string) error {
		switch id {
		case dialID:
			if routed() {
				return nil
			}
			return fmt.Errorf("dial is reached by picking a model from dial's catalog: dial gemini model <provider/model>")
		case "custom":
			if current() != "custom" {
				return fmt.Errorf("custom means whatever GOOGLE_GEMINI_BASE_URL is already in %s; set it there", envPath)
			}
			return nil
		case "", "google", "vertex", "api-key":
		default:
			return fmt.Errorf("unknown provider %q", id)
		}
		wasRouted := routed()
		if err := leave(); err != nil {
			return err
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
		}
		if wasRouted {
			// leave already put the base URL back as it was
			return nil
		}
		return edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL")
	}

	modelGet := jsonGet(path, "model.name")

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
				Key: "auth", Label: "auth",
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
					if routed() {
						out = append([]Option{{Value: dialID, Label: "dial",
							Note: "through dial's gateway — pick another model to leave it"}}, out...)
					}
					return out
				},
			},
			{
				Key: "model", Label: "model",
				Get: modelGet,
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
				Key: "provider", Label: "provider",
				Get: func() string {
					if routed() {
						if p, _, ok := strings.Cut(modelGet(), "/"); ok {
							return p
						}
					}
					return "google"
				},
				Set: func(v string) error {
					if v == "" || v == "google" {
						return setModel("")
					}
					for _, e := range provider.Catalog() {
						if e.Provider.ID == v {
							return setModel(e.ID)
						}
					}
					return fmt.Errorf("no models of %q are picked in dial's providers", v)
				},
				Options: func(map[string]string) []Option {
					out := []Option{{Value: "google", Label: "Google", Note: "Gemini CLI's own sign-in or key"}}
					seen := map[string]bool{"google": true}
					for _, e := range provider.Catalog() {
						p := e.Provider
						if seen[p.ID] {
							continue
						}
						seen[p.ID] = true
						out = append(out, Option{Value: p.ID, Label: p.Name, Note: viaNote(p), Icon: p.Icon})
					}
					return out
				},
			},
		},
	}
}

// setOrDelEnv sets a .env key, or removes it when val is empty.
func setOrDelEnv(path, key, val string) error {
	if val == "" {
		return edit.DelEnvFile(path, key)
	}
	return edit.SetEnvFile(path, edit.KV{Path: key, Value: val})
}
