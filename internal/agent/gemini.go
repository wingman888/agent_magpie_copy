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
// gateway: GOOGLE_GEMINI_BASE_URL points at dial, GEMINI_API_KEY is dial's
// token, selectedType is gemini-api-key and model.name is the reference.
// What those keys held before is stashed, so picking a native model (or
// Default) puts them back exactly as they were.

// Stash keys for the values GOOGLE_GEMINI_BASE_URL, GEMINI_API_KEY and
// selectedType held before dial routed the CLI through the gateway.
const (
	geminiAuthStash = "gemini.auth"
	geminiKeyStash  = "gemini.key"
	geminiBaseStash = "gemini.base"
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

	// routed: the CLI is pointed at the gateway through a catalog model.
	routed := func() bool { return base() == gateway.URL() && auth() == "gemini-api-key" }

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

	// leave undoes routing: the values from before dial took over come back,
	// and whatever was not there is removed. The catalog model goes too.
	leave := func() error {
		if !routed() {
			return nil
		}
		prevAuth, prevKey, prevBase := unstash(geminiAuthStash), unstash(geminiKeyStash), unstash(geminiBaseStash)
		var err error
		if prevAuth == "" {
			err = edit.DelJSON(path, "security.auth.selectedType")
		} else {
			err = edit.SetJSON(path, edit.KV{Path: "security.auth.selectedType", Value: prevAuth})
		}
		if err != nil {
			return err
		}
		if prevKey == "" {
			err = edit.DelEnvFile(envPath, "GEMINI_API_KEY")
		} else {
			err = edit.SetEnvFile(envPath, edit.KV{Path: "GEMINI_API_KEY", Value: prevKey})
		}
		if err != nil {
			return err
		}
		if prevBase == "" {
			err = edit.DelEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL")
		} else {
			err = edit.SetEnvFile(envPath, edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: prevBase})
		}
		if err != nil {
			return err
		}
		return edit.DelJSON(path, "model.name")
	}

	// route points the CLI at the gateway for one catalog model. The values
	// already there are stashed only on the first route, never overwritten by
	// the gateway's own values when switching between catalog models.
	route := func(model string) error {
		if !routed() {
			stash(map[string]string{geminiAuthStash: auth(), geminiKeyStash: envKey(), geminiBaseStash: base()})
		}
		if err := edit.SetJSON(path,
			edit.KV{Path: "security.auth.selectedType", Value: "gemini-api-key"},
			edit.KV{Path: "model.name", Value: model}); err != nil {
			return err
		}
		return edit.SetEnvFile(envPath,
			edit.KV{Path: "GEMINI_API_KEY", Value: gateway.Token},
			edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: gateway.URL()})
	}

	use := func(id string) error {
		if id == dialID {
			if routed() {
				return nil
			}
			return fmt.Errorf("dial is reached by picking a dial model: dial gemini model <provider/model>")
		}
		if id == "custom" {
			if current() != "custom" {
				return fmt.Errorf("custom means whatever GOOGLE_GEMINI_BASE_URL is already in %s; set it there", envPath)
			}
			return nil
		}
		wasRouted := routed()
		if wasRouted {
			// leaving dial: put back what the CLI had, then apply the choice
			if err := leave(); err != nil {
				return err
			}
		}
		switch id {
		case "":
			if wasRouted {
				return nil // leave() already restored the CLI's own setting
			}
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
						out = append(out, Option{Value: dialID, Note: "through dial's gateway — pick a dial model to get here"})
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
				Set: func(v string) error {
					if isDial(v) {
						return route(v)
					}
					// a native model, or Default: the CLI's own settings come back first
					if err := leave(); err != nil {
						return err
					}
					return jsonSet(path, "model.name")(v)
				},
				Options: func(map[string]string) []Option {
					var ms []catalog.Model
					for _, m := range catalog.Provider("google") {
						if strings.HasPrefix(m.ID, "gemini-") {
							ms = append(ms, m)
						}
					}
					// catalog models reach the CLI through the gateway
					return append(options(ms, ""), viaDial("")...)
				},
			},
		},
	}
}
