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

// Gemini CLI only speaks Google's own API, so "auth" here is how it
// authenticates: Google sign-in, a Gemini API key, or Vertex AI. The choice
// lives in settings.json (security.auth.selectedType). A Google provider
// added in dial lends its key through ~/.gemini/.env, which the CLI loads.
//
// Picking a dial catalog model ("fake/m1") routes the CLI through the
// gateway: base URL and key go into .env, the API-key auth type into
// settings.json. What was there before is stashed so that picking a
// native model or Default puts it back.

const (
	geminiSelType = "security.auth.selectedType"
	geminiModel   = "model.name"
	geminiKeyEnv  = "GEMINI_API_KEY"
	geminiBaseEnv = "GOOGLE_GEMINI_BASE_URL"
	// stashUnset records that a value was absent before dial routed the CLI.
	stashUnset = "\x00unset"
)

// geminiStash lists the stash keys holding what routing displaced.
var geminiStash = []string{"gemini.selectedType", "gemini.apiKey", "gemini.baseUrl", "gemini.model"}

func gemini(home string) *Agent {
	dir := filepath.Join(home, ".gemini")
	path := filepath.Join(dir, "settings.json")
	envPath := filepath.Join(dir, ".env")
	jget := func(k string) string { v, _ := edit.GetJSON(path, k); return v }
	eget := func(k string) string { v, _ := edit.GetEnvFile(envPath, k); return v }
	auth := func() string { return jget(geminiSelType) }
	base := func() string { return eget(geminiBaseEnv) }
	envKey := func() string { return eget(geminiKeyEnv) }
	dialKey := func() string {
		for _, id := range []string{"google", "gemini"} {
			if p, err := provider.Find(id); err == nil && p.Key != "" {
				return p.Key
			}
		}
		return ""
	}
	// routed: the CLI's .env points at the gateway and it uses an API key.
	routed := func() bool {
		return strings.TrimSuffix(base(), "/") == gateway.URL() && auth() == "gemini-api-key"
	}
	// catalogRef is the model name when it is a dial catalog reference.
	catalogRef := func() string {
		if m := jget(geminiModel); strings.Contains(m, "/") {
			return m
		}
		return ""
	}

	// enter routes the CLI through the gateway for one catalog model.
	enter := func(ref string) error {
		if !routed() {
			sel, ok1 := edit.GetJSON(path, geminiSelType)
			key, ok2 := edit.GetEnvFile(envPath, geminiKeyEnv)
			b, ok3 := edit.GetEnvFile(envPath, geminiBaseEnv)
			m, ok4 := edit.GetJSON(path, geminiModel)
			stash(map[string]string{
				"gemini.selectedType": stashed(sel, ok1),
				"gemini.apiKey":       stashed(key, ok2),
				"gemini.baseUrl":      stashed(b, ok3),
				"gemini.model":        stashed(m, ok4),
			})
		}
		if err := edit.SetEnvFile(envPath,
			edit.KV{Path: geminiBaseEnv, Value: gateway.URL()},
			edit.KV{Path: geminiKeyEnv, Value: gateway.Token},
		); err != nil {
			return err
		}
		return edit.SetJSON(path,
			edit.KV{Path: geminiSelType, Value: "gemini-api-key"},
			edit.KV{Path: geminiModel, Value: ref},
		)
	}

	// restore puts back the auth type, key and base URL from before routing.
	// Without a record (routed by hand) it removes what routing would have
	// written: the base URL, the auth type, and the key only if it is ours.
	restore := func() error {
		recorded := stashLoad()["gemini.baseUrl"] != ""
		sel, selOK := unstashed("gemini.selectedType")
		key, keyOK := unstashed("gemini.apiKey")
		b, baseOK := unstashed("gemini.baseUrl")
		unstash("gemini.model")
		if !recorded {
			selOK, baseOK = false, false
			if cur := envKey(); cur != gateway.Token {
				key, keyOK = cur, cur != ""
			} else {
				key, keyOK = "", false
			}
		}
		if selOK {
			if err := edit.SetJSON(path, edit.KV{Path: geminiSelType, Value: sel}); err != nil {
				return err
			}
		} else if err := edit.DelJSON(path, geminiSelType); err != nil {
			return err
		}
		var set []edit.KV
		var del []string
		if keyOK {
			set = append(set, edit.KV{Path: geminiKeyEnv, Value: key})
		} else {
			del = append(del, geminiKeyEnv)
		}
		if baseOK {
			set = append(set, edit.KV{Path: geminiBaseEnv, Value: b})
		} else {
			del = append(del, geminiBaseEnv)
		}
		if len(set) > 0 {
			if err := edit.SetEnvFile(envPath, set...); err != nil {
				return err
			}
		}
		return edit.DelEnvFile(envPath, del...)
	}

	// leave undoes routing entirely, catalog model included.
	leave := func() error {
		if err := restore(); err != nil {
			return err
		}
		return edit.DelJSON(path, geminiModel)
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

	modelSet := func(v string) error {
		switch {
		case v == "":
			if routed() {
				return leave()
			}
			return edit.DelJSON(path, geminiModel)
		case strings.Contains(v, "/"):
			if !isDial(v) {
				return fmt.Errorf("%q is not in dial's catalog — dial providers lists the models", v)
			}
			return enter(v)
		}
		// a native model: back to the CLI's own auth
		if routed() {
			if err := restore(); err != nil {
				return err
			}
		} else {
			forget(geminiStash...)
		}
		return edit.SetJSON(path, edit.KV{Path: geminiModel, Value: v})
	}

	authSet := func(v string) error {
		switch v {
		case "dial":
			if routed() {
				return nil
			}
			return errors.New("dial is reached by picking a model from dial's catalog: dial gemini model <provider/model>")
		case "custom":
			if current() != "custom" {
				return fmt.Errorf("custom means whatever GOOGLE_GEMINI_BASE_URL is already in %s; set it there", envPath)
			}
			return nil
		case "", "google", "vertex", "api-key":
		default:
			return fmt.Errorf("unknown provider %q", v)
		}
		if routed() {
			if err := leave(); err != nil {
				return err
			}
			if v == "" {
				return nil
			}
		}
		switch v {
		case "":
			// back to the CLI's own first-run choice
			if err := edit.DelJSON(path, geminiSelType); err != nil {
				return err
			}
		case "google":
			if err := edit.SetJSON(path, edit.KV{Path: geminiSelType, Value: "oauth-personal"}); err != nil {
				return err
			}
		case "vertex":
			if err := edit.SetJSON(path, edit.KV{Path: geminiSelType, Value: "vertex-ai"}); err != nil {
				return err
			}
		case "api-key":
			if err := edit.SetJSON(path, edit.KV{Path: geminiSelType, Value: "gemini-api-key"}); err != nil {
				return err
			}
			if k := dialKey(); k != "" && envKey() != k {
				if err := edit.SetEnvFile(envPath, edit.KV{Path: geminiKeyEnv, Value: k}); err != nil {
					return err
				}
			}
		}
		return edit.DelEnvFile(envPath, geminiBaseEnv)
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
				Key: "model", Label: "model",
				Get: jsonGet(path, geminiModel),
				Set: modelSet,
				Options: func(map[string]string) []Option {
					var ms []catalog.Model
					for _, m := range catalog.Provider("google") {
						if strings.HasPrefix(m.ID, "gemini-") {
							ms = append(ms, m)
						}
					}
					// Google's own models are the native list above; the
					// rest of the catalog goes through the gateway.
					out := options(ms, "")
					return append(out, viaDialExcept("", func(p provider.Provider) bool { return p.ID == "google" })...)
				},
			},
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
					if routed() {
						out = append([]Option{{Value: "dial", Label: "dial", Icon: "gemini-color", Note: catalogRef() + " via the gateway"}}, out...)
					}
					if current() == "custom" {
						out = append(out, Option{Value: "custom", Note: hostOf(base()) + " (from .env)"})
					}
					return out
				},
			},
			{
				Key: "provider", Label: "provider",
				Get: func() string {
					ref := catalogRef()
					p, _, _ := strings.Cut(ref, "/")
					if ref == "" {
						return ""
					}
					return p
				},
				Set: func(v string) error {
					if v == "" {
						return modelSet("")
					}
					if strings.HasPrefix(catalogRef(), v+"/") {
						return nil
					}
					for _, e := range provider.Catalog() {
						if e.Provider.ID == v {
							return modelSet(e.ID)
						}
					}
					return fmt.Errorf("no models from provider %q — dial providers lists them", v)
				},
				Options: func(map[string]string) []Option {
					var out []Option
					seen := map[string]bool{}
					for _, e := range provider.Catalog() {
						if e.Provider.ID == "google" || seen[e.Provider.ID] {
							continue
						}
						seen[e.Provider.ID] = true
						out = append(out, Option{Value: e.Provider.ID, Label: e.Provider.Name, Icon: e.Provider.Icon,
							Note: "via dial", Group: ""})
					}
					return out
				},
			},
		},
	}
}

// stashed encodes one displaced value: the value, or stashUnset when absent.
func stashed(v string, ok bool) string {
	if !ok {
		return stashUnset
	}
	return v
}

// unstashed takes a displaced value back out of the stash. ok is false when
// the value was absent before routing, or there is no record of it.
func unstashed(key string) (string, bool) {
	v := unstash(key)
	if v == "" || v == stashUnset {
		return "", false
	}
	return v, true
}
