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
// Picking a catalog model ("provider/model") routes the CLI through the
// gateway: the base URL and key go to .env, selectedType becomes
// gemini-api-key, and the auth field reads "dial". Whatever those keys held
// before is stashed and put back when the CLI is pointed elsewhere.

// geminiStash names the stash entries for one .env or settings.json key.
func geminiStash(name string) (value, present string) {
	return "gemini." + name, "gemini." + name + ".set"
}

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
	routed := func() bool { return base() == gateway.URL() && auth() == "gemini-api-key" }

	// route stashes what the three keys held (absent ones are marked as
	// absent) and points the CLI at the gateway.
	route := func(ref string) error {
		if !routed() {
			saved := map[string]string{}
			save := func(name, v string, ok bool) {
				val, pres := geminiStash(name)
				saved[val], saved[pres] = "", ""
				if ok {
					saved[val], saved[pres] = v, "1"
				}
			}
			av, aok := edit.GetJSON(path, "security.auth.selectedType")
			kv, kok := edit.GetEnvFile(envPath, "GEMINI_API_KEY")
			bv, bok := edit.GetEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL")
			save("selectedType", av, aok)
			save("GEMINI_API_KEY", kv, kok)
			save("GOOGLE_GEMINI_BASE_URL", bv, bok)
			stash(saved)
		}
		if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: "gemini-api-key"}); err != nil {
			return err
		}
		if err := edit.SetEnvFile(envPath,
			edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: gateway.URL()},
			edit.KV{Path: "GEMINI_API_KEY", Value: gateway.Token},
		); err != nil {
			return err
		}
		return edit.SetJSON(path, edit.KV{Path: "model.name", Value: ref})
	}

	// unroute puts back what route stashed; keys that were absent are removed.
	unroute := func() error {
		if !routed() {
			return nil
		}
		back := func(name string) (string, bool) {
			val, pres := geminiStash(name)
			v := unstash(val)
			return v, unstash(pres) != ""
		}
		av, aok := back("selectedType")
		kv, kok := back("GEMINI_API_KEY")
		bv, bok := back("GOOGLE_GEMINI_BASE_URL")
		if aok {
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: av}); err != nil {
				return err
			}
		} else if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
			return err
		}
		if kok {
			if err := edit.SetEnvFile(envPath, edit.KV{Path: "GEMINI_API_KEY", Value: kv}); err != nil {
				return err
			}
		} else if err := edit.DelEnvFile(envPath, "GEMINI_API_KEY"); err != nil {
			return err
		}
		if bok {
			return edit.SetEnvFile(envPath, edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: bv})
		}
		return edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL")
	}

	current := func() string {
		if routed() {
			return "dial"
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
		switch id {
		case "dial":
			if routed() {
				return nil
			}
			return fmt.Errorf("dial is chosen by picking a catalog model (provider/model) for gemini's model field")
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
		if wasRouted {
			// leaving the gateway: put back what dial overwrote first
			if err := unroute(); err != nil {
				return err
			}
			if id == "" {
				return nil
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
		}
		if wasRouted {
			return nil // unroute already restored the base URL
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
					if current() == "custom" {
						out = append(out, Option{Value: "custom", Note: hostOf(base()) + " (from .env)"})
					}
					if routed() {
						out = append(out, Option{Value: "dial", Label: "dial", Icon: "gemini-color", Note: "through the gateway · pick a catalog model to change"})
					}
					return out
				},
			},
			{
				Key: "model", Label: "model",
				Get: jsonGet(path, "model.name"),
				Set: func(v string) error {
					switch {
					case v == "":
						if err := unroute(); err != nil {
							return err
						}
						return edit.DelJSON(path, "model.name")
					case strings.Contains(v, "/"):
						return route(v)
					default:
						if err := unroute(); err != nil {
							return err
						}
						return edit.SetJSON(path, edit.KV{Path: "model.name", Value: v})
					}
				},
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
		},
	}
}
