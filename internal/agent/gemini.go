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

	// routed: the CLI talks to dial's gateway with a Gemini API key.
	routed := func() bool { return base() == gateway.URL() && auth() == "gemini-api-key" }
	// restore puts auth type, key and base URL back as they were before
	// dial routed the CLI (absent values are removed).
	restore := func() error {
		if !routed() {
			return nil
		}
		st := stashLoad()
		put := func(v string, set func(string) error, del func() error) error {
			if v == "" {
				return del()
			}
			return set(v)
		}
		if err := put(st["gemini.auth"],
			func(v string) error { return edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: v}) },
			func() error { return edit.DelJSON(path, "security.auth.selectedType") }); err != nil {
			return err
		}
		if err := put(st["gemini.key"],
			func(v string) error { return edit.SetEnvFile(envPath, edit.KV{Path: "GEMINI_API_KEY", Value: v}) },
			func() error { return edit.DelEnvFile(envPath, "GEMINI_API_KEY") }); err != nil {
			return err
		}
		if err := put(st["gemini.base"],
			func(v string) error { return edit.SetEnvFile(envPath, edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: v}) },
			func() error { return edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL") }); err != nil {
			return err
		}
		forget("gemini.auth", "gemini.key", "gemini.base")
		return nil
	}
	setModel := func(v string) error {
		switch {
		case v == "":
			if err := restore(); err != nil {
				return err
			}
			return edit.DelJSON(path, "model.name")
		case strings.Contains(v, "/"):
			if !routed() {
				stash(map[string]string{"gemini.auth": auth(), "gemini.key": envKey(), "gemini.base": base()})
			}
			if err := edit.SetEnvFile(envPath,
				edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: gateway.URL()},
				edit.KV{Path: "GEMINI_API_KEY", Value: gateway.Token}); err != nil {
				return err
			}
			return edit.SetJSON(path,
				edit.KV{Path: "security.auth.selectedType", Value: "gemini-api-key"},
				edit.KV{Path: "model.name", Value: v})
		}
		if err := restore(); err != nil {
			return err
		}
		return edit.SetJSON(path, edit.KV{Path: "model.name", Value: v})
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
			if !routed() {
				return fmt.Errorf("pick a dial model to route Gemini CLI through dial")
			}
			return nil
		}
		if err := restore(); err != nil {
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
				Key: "provider", Label: "provider",
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
						out = append(out, Option{Value: "dial", Note: "routed through dial's gateway"})
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
					own := group("Gemini", options(ms, ""))
					return append(own, viaDial("")...)
				},
			},
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
					if routed() {
						out = append(out, Option{Value: "dial", Note: "routed through dial's gateway"})
					}
					if current() == "custom" {
						out = append(out, Option{Value: "custom", Note: hostOf(base()) + " (from .env)"})
					}
					return out
				},
			},
		},
	}
}
