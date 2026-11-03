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

// Gemini CLI only speaks Google's own API, so its auth is one of: Google
// sign-in, a Gemini API key, or Vertex AI. The choice lives in settings.json
// (security.auth.selectedType). A Google provider added in dial lends its key
// through ~/.gemini/.env, which the CLI loads.
//
// Picking a dial catalog model ("provider/model") routes the CLI through the
// gateway: GOOGLE_GEMINI_BASE_URL and GEMINI_API_KEY go to ~/.gemini/.env and
// selectedType becomes gemini-api-key. What those keys held before is stashed
// so picking a native model (or Default) puts it back.

// geminiStash keys: the values Gemini CLI's config held before dial routed it.
const (
	geminiStashAuth = "gemini.auth"
	geminiStashKey  = "gemini.key"
	geminiStashBase = "gemini.base"
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

	// routed: the CLI sends its requests to the gateway with a catalog model.
	routed := func() bool {
		return base() == gateway.URL() && auth() == "gemini-api-key"
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

	// route points the CLI at the gateway and selects a catalog model.
	route := func(ref string) error {
		if !routed() {
			stash(map[string]string{
				geminiStashAuth: auth(),
				geminiStashKey:  envKey(),
				geminiStashBase: base(),
			})
		}
		if err := edit.SetJSON(path,
			edit.KV{Path: "security.auth.selectedType", Value: "gemini-api-key"},
			edit.KV{Path: "model.name", Value: ref},
		); err != nil {
			return err
		}
		return edit.SetEnvFile(envPath,
			edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: gateway.URL()},
			edit.KV{Path: "GEMINI_API_KEY", Value: gateway.Token},
		)
	}

	// unroute undoes route: auth, key and base URL go back to what they were
	// before, and a value that was absent is removed. No-op unless routed.
	unroute := func() error {
		if !routed() {
			return nil
		}
		if v := unstash(geminiStashAuth); v != "" {
			if err := edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: v}); err != nil {
				return err
			}
		} else if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
			return err
		}
		if v := unstash(geminiStashKey); v != "" {
			if err := edit.SetEnvFile(envPath, edit.KV{Path: "GEMINI_API_KEY", Value: v}); err != nil {
				return err
			}
		} else if err := edit.DelEnvFile(envPath, "GEMINI_API_KEY"); err != nil {
			return err
		}
		if v := unstash(geminiStashBase); v != "" {
			return edit.SetEnvFile(envPath, edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: v})
		}
		return edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL")
	}

	authSet := func(v string) error {
		switch v {
		case "":
			// Default: back to the CLI's own choice. Routed, that means the
			// stashed config and no pinned model.
			if routed() {
				if err := unroute(); err != nil {
					return err
				}
				return edit.DelJSON(path, "model.name")
			}
			if err := edit.DelJSON(path, "security.auth.selectedType"); err != nil {
				return err
			}
			return edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL")
		case "dial":
			// dial is only ever reached by picking a catalog model.
			if routed() {
				return nil
			}
			return fmt.Errorf("dial is set by choosing a dial model, e.g. `dial gemini model <provider/model>`")
		case "custom":
			if current() != "custom" {
				return fmt.Errorf("custom means whatever GOOGLE_GEMINI_BASE_URL is already in %s; set it there", envPath)
			}
			return nil
		case "google", "vertex", "api-key":
		default:
			return fmt.Errorf("unknown auth %q", v)
		}
		if err := unroute(); err != nil {
			return err
		}
		switch v {
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

	modelSet := func(v string) error {
		switch {
		case v == "":
			if err := unroute(); err != nil {
				return err
			}
			return edit.DelJSON(path, "model.name")
		case strings.Contains(v, "/"):
			if !isDial(v) {
				return fmt.Errorf("%q is not a model in dial's catalog (see `dial models`)", v)
			}
			return route(v)
		case !strings.HasPrefix(v, "gemini"):
			// a bare id that is not one of Gemini's own names: it must name
			// exactly one catalog entry, which then becomes the routed model
			ref, err := bareRef(v)
			if err != nil {
				return err
			}
			return route(ref)
		}
		// a native Gemini model: back to Google's own endpoint first
		if err := unroute(); err != nil {
			return err
		}
		return edit.SetJSON(path, edit.KV{Path: "model.name", Value: v})
	}

	// the provider field names the dial provider serving the routed model
	providerGet := func() string {
		if !routed() {
			return ""
		}
		p, _, _ := strings.Cut(modelName(), "/")
		return p
	}
	providerSet := func(v string) error {
		if v == "" {
			return modelSet("")
		}
		// a bare value that is an auth choice belongs to the auth field:
		// profiles saved before the split stored auth under this key.
		switch v {
		case "google", "vertex", "api-key", "custom", "dial":
			return authSet(v)
		}
		if routed() {
			if p, _, ok := strings.Cut(modelName(), "/"); ok && p == v {
				return nil // already served by this provider
			}
		}
		// prefer the same model id from the new provider, else its first model
		want := ""
		if _, m, ok := strings.Cut(modelName(), "/"); ok {
			want = m
		}
		ref := ""
		for _, e := range provider.Catalog() {
			if e.Provider.ID != v {
				continue
			}
			if ref == "" || e.Model == want {
				ref = e.ID
			}
			if e.Model == want {
				break
			}
		}
		if ref == "" {
			return fmt.Errorf("no models from %q in dial's catalog", v)
		}
		return route(ref)
	}
	providerOptions := func(map[string]string) []Option {
		var out []Option
		seen := map[string]bool{}
		for _, e := range provider.Catalog() {
			p := e.Provider
			if seen[p.ID] {
				continue
			}
			seen[p.ID] = true
			note := p.Name
			if p.Account != nil {
				note = p.Account.User
			}
			out = append(out, Option{Value: p.ID, Label: p.Name, Note: note, Icon: p.Icon})
		}
		return out
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
				Key: "auth", Label: "auth",
				Get: current,
				Set: authSet,
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
					return out
				},
			},
			{
				Key: "model", Label: "model",
				Get: modelName,
				Set: modelSet,
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
			{
				Key: "provider", Label: "provider",
				Get:     providerGet,
				Set:     providerSet,
				Options: providerOptions,
			},
		},
	}
}
