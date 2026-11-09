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
// Through dial the CLI is pointed at the gateway with an API key, and its
// model is any catalog reference ("provider/model"). Going back to a native
// model (or Default) restores what the config said before dial took over;
// those values are kept in the stash, keyed by the names below.

const (
	geminiSelected = "gemini.selectedType"
	geminiKey      = "gemini.GEMINI_API_KEY"
	geminiBase     = "gemini.GOOGLE_GEMINI_BASE_URL"
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

	// routed: the CLI talks to this gateway with an API key.
	routed := func() bool {
		return base() == gateway.URL() && auth() == "gemini-api-key"
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

	// route points the CLI at the gateway. The first time, it remembers what
	// the CLI had, so unroute can put it back.
	route := func() error {
		if !routed() {
			stash(map[string]string{
				geminiSelected: auth(),
				geminiKey:      envKey(),
				geminiBase:     base(),
			})
		}
		if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: "gemini-api-key"}); err != nil {
			return err
		}
		return edit.SetEnvFile(envPath,
			edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: gateway.URL()},
			edit.KV{Path: "GEMINI_API_KEY", Value: gateway.Token},
		)
	}

	// unroute restores the values stashed by route. A value that was absent
	// is stashed as "", which means the key is removed again.
	unroute := func() error {
		sel := unstash(geminiSelected)
		key := unstash(geminiKey)
		baseURL := unstash(geminiBase)
		if sel == "" {
			if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
				return err
			}
		} else if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: sel}); err != nil {
			return err
		}
		if key == "" {
			if err := edit.DelEnvFile(envPath, "GEMINI_API_KEY"); err != nil {
				return err
			}
		} else if err := edit.SetEnvFile(envPath, edit.KV{Path: "GEMINI_API_KEY", Value: key}); err != nil {
			return err
		}
		if baseURL == "" {
			return edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL")
		}
		return edit.SetEnvFile(envPath, edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: baseURL})
	}

	use := func(id string) error {
		if id == dialID {
			if routed() {
				return nil
			}
			return fmt.Errorf("dial is reached by picking a model from its catalog: dial gemini model <provider>/<model>")
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

	// setModel: a value with "/" is a catalog reference and routes through
	// the gateway; anything else is a native Gemini model; "" is Default.
	setModel := func(v string) error {
		switch {
		case v == "":
			if routed() {
				if err := unroute(); err != nil {
					return err
				}
			}
			return edit.DelJSON(path, "model.name")
		case strings.Contains(v, "/"):
			if !isDial(v) {
				return fmt.Errorf("%q is not in dial's catalog (dial providers lists them)", v)
			}
			if err := route(); err != nil {
				return err
			}
			return edit.SetJSON(path, edit.KV{Path: "model.name", Value: v})
		default:
			if routed() {
				if err := unroute(); err != nil {
					return err
				}
			}
			return edit.SetJSON(path, edit.KV{Path: "model.name", Value: v})
		}
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
						out = append(out, Option{Value: dialID, Label: "dial", Note: "through the gateway · pick a dial model below to change"})
					}
					if current() == "custom" {
						out = append(out, Option{Value: "custom", Note: hostOf(base()) + " (from .env)"})
					}
					return out
				},
			},
			{
				Key: "model", Label: "model",
				Get: jsonGet(path, "model.name"),
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
		},
	}
}
