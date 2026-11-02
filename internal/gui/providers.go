package gui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// The providers page: the vendors the user added, the presets they can add
// with one key, the gateway that fronts them, and who is routed where.

type modelJSON struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Efforts []string `json:"efforts,omitempty"`
	On      bool     `json:"on"` // exposed to agents
}

type providerJSON struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Icon      string            `json:"icon"`
	Preset    string            `json:"preset"`
	Host      string            `json:"host"`
	Chat      string            `json:"chat"`
	Responses string            `json:"responses"`
	Anthropic string            `json:"anthropic"`
	Catalog   string            `json:"catalog"`
	Website   string            `json:"website"`
	KeysURL   string            `json:"keysUrl"`
	Headers   map[string]string `json:"headers,omitempty"`
	Key       struct {
		Set      bool   `json:"set"`
		Masked   string `json:"masked"`
		Optional bool   `json:"optional"`
	} `json:"key"`
	Ready     bool               `json:"ready"`
	Chosen    []string           `json:"chosen"`   // the user's explicit picks, if any
	Fallback  []string           `json:"fallback"` // where requests go when this one can't take them
	Models    []modelJSON        `json:"models"`   // everything the vendor lists, exposed ones flagged
	Exposed   int                `json:"exposed"`  // how many reach the agents
	Fetched   string             `json:"fetched"`  // "3h ago" when the list came from the vendor
	Agents    []providerAgent    `json:"agents"`   // detected agents, current ones flagged
	Sponsored bool               `json:"sponsored"`
	KeyList   []provider.KeyInfo `json:"keyList"`           // its keys, in the order requests try them
	Account   *accountJSON       `json:"account,omitempty"` // a signed-in agent, see provider.Account
}

type accountJSON struct {
	provider.Account
	Agent string `json:"agent"`     // the agent's id
	Name  string `json:"agentName"` // the agent's name, for "from Codex CLI's sign-in"
	Icon  string `json:"agentIcon"`
	// Logins are the agent's accounts magpie remembers, to switch between
	Logins []provider.Login `json:"logins,omitempty"`
}

type providerAgent struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Icon    string `json:"icon"`
	Current bool   `json:"current"` // this agent is on one of this provider's models now
	Model   string `json:"model,omitempty"`
}

type presetJSON struct {
	provider.PresetDef
	Added bool `json:"added"`
}

type gatewayJSON struct {
	URL     string         `json:"url"`
	Running bool           `json:"running"`
	Mine    bool           `json:"mine"` // this process serves it
	Models  int            `json:"models"`
	Calls   []gateway.Call `json:"calls"`
}

type excludedJSON struct {
	provider.Exclusion
	Name string `json:"agentName"`
	Icon string `json:"agentIcon"`
}

type providersJSON struct {
	Providers []providerJSON `json:"providers"`
	Presets   []presetJSON   `json:"presets"`
	Excluded  []excludedJSON `json:"excluded"` // sign-ins magpie found but will not share
	Gateway   gatewayJSON    `json:"gateway"`
}

// currentProvider reads which provider (and model) an agent is routed to now.
func currentProvider(a *agent.Agent) (string, string) {
	if len(a.Fields) == 0 {
		return "", ""
	}
	v := strings.TrimPrefix(a.Fields[0].Get(), "magpie/")
	if pid, model, ok := strings.Cut(v, "/"); ok {
		if _, err := provider.Find(pid); err == nil {
			return pid, model
		}
	}
	return "", ""
}

func providerInfo(p provider.Provider, agents []*agent.Agent) providerJSON {
	out := providerJSON{
		ID: p.ID, Name: p.Name, Icon: p.Icon, Preset: p.Preset, Host: p.Host(),
		Chat: p.Chat, Responses: p.Responses, Anthropic: p.Anthropic,
		Catalog: p.Catalog, Website: p.Website, KeysURL: p.KeysURL,
		Headers: p.Headers,
		Ready:   p.Ready(), Chosen: p.Models, Models: []modelJSON{}, Agents: []providerAgent{},
		Fallback: p.Fallback,
	}
	if out.Fallback == nil {
		out.Fallback = []string{}
	}
	if out.Chosen == nil {
		out.Chosen = []string{}
	}
	if pr := provider.Preset(p.Preset); pr != nil {
		out.Sponsored = pr.Sponsored
		out.Key.Optional = pr.NoKey
	}
	out.Key.Set = p.Key != ""
	out.Key.Masked = provider.Mask(p.Key)
	out.KeyList = p.KeyList()
	if out.KeyList == nil {
		out.KeyList = []provider.KeyInfo{}
	}
	if !out.Key.Set && p.Ready() {
		out.Key.Optional = true
	}
	if a := p.Account; a != nil {
		out.Account = &accountJSON{Account: *a, Agent: a.Agent, Name: a.Agent, Icon: "generic"}
		if ag, err := agent.Find(a.Agent); err == nil {
			out.Account.Name, out.Account.Icon = ag.Name, ag.Icon
		}
		out.Account.Logins = provider.Logins(a.Agent)
	}
	exposed := map[string]bool{}
	for _, m := range p.Exposed() {
		exposed[m.ID] = true
	}
	seen := map[string]bool{}
	for _, m := range p.Available() {
		seen[m.ID] = true
		out.Models = append(out.Models, modelJSON{ID: m.ID, Name: m.Name, Efforts: m.Efforts, On: exposed[m.ID]})
	}
	// picks the vendor list does not know go first, so they are visible
	for _, m := range p.Exposed() {
		if !seen[m.ID] {
			out.Models = append([]modelJSON{{ID: m.ID, Name: m.Name, Efforts: m.Efforts, On: true}}, out.Models...)
		}
	}
	out.Exposed = len(exposed)
	if t, ok := p.Fetched(); ok {
		out.Fetched = ago(t)
	}
	for _, a := range agents {
		pid, model := currentProvider(a)
		out.Agents = append(out.Agents, providerAgent{ID: a.ID, Name: a.Name, Icon: a.Icon, Current: pid == p.ID, Model: model})
	}
	return out
}

func providersState(gw *gateway.Server) providersJSON {
	agents := agent.Detected()
	s := providersJSON{Providers: []providerJSON{}, Presets: []presetJSON{}, Excluded: []excludedJSON{}}
	for _, x := range provider.Excluded() {
		e := excludedJSON{Exclusion: x, Name: x.Agent, Icon: "generic"}
		if a, err := agent.Find(x.Agent); err == nil {
			e.Name, e.Icon = a.Name, a.Icon
		}
		s.Excluded = append(s.Excluded, e)
	}
	have := map[string]bool{}
	for _, p := range provider.All() {
		have[p.ID] = true
		s.Providers = append(s.Providers, providerInfo(p, agents))
	}
	for _, pr := range provider.Presets() {
		s.Presets = append(s.Presets, presetJSON{PresetDef: pr, Added: have[pr.ID]})
	}
	s.Gateway = gatewayJSON{URL: gateway.URL(), Models: len(provider.Catalog()), Calls: []gateway.Call{}}
	if gw != nil {
		s.Gateway.Running, s.Gateway.Mine = true, true
		s.Gateway.Calls = gw.Recent()
	} else {
		s.Gateway.Running = gateway.Running()
	}
	return s
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Round(time.Minute).Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Round(time.Hour).Hours()))
	default:
		return t.Format("Jan 2")
	}
}

func providerRoutes(mux *http.ServeMux, w Windows, gw *gateway.Server) {
	mux.HandleFunc("GET /api/providers", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, providersState(gw))
	})
	// a picture for a provider, picked in the editor: kept by content before
	// the provider is saved, which then points at it
	mux.HandleFunc("POST /api/icons", func(rw http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(io.LimitReader(r.Body, provider.MaxIcon+1))
		if err != nil {
			fail(rw, err)
			return
		}
		icon, err := provider.StoreIcon(b)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]string{"icon": icon})
	})
	mux.HandleFunc("GET /api/icons/{name}", func(rw http.ResponseWriter, r *http.Request) {
		f := provider.IconFile(r.PathValue("name"))
		if f == "" {
			http.NotFound(rw, r)
			return
		}
		// an SVG is only ever drawn as an image, never run
		rw.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		rw.Header().Set("Cache-Control", "max-age=31536000, immutable")
		http.ServeFile(rw, r, f)
	})
	mux.HandleFunc("POST /api/provider/{action}", func(rw http.ResponseWriter, r *http.Request) {
		var in provider.Provider
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		switch r.PathValue("action") {
		case "save":
			// a preset needs nothing but the key; a saved provider keeps
			// its key when the form left it blank
			if pr, err := provider.FromPreset(in.Preset); err == nil && in.Chat == "" && in.Responses == "" && in.Anthropic == "" {
				pr.Key, pr.Models, pr.Fallback = in.Key, in.Models, in.Fallback
				if in.Name != "" {
					pr.Name = in.Name
				}
				if in.ID != "" {
					pr.ID = in.ID
				}
				in = pr
			}
			old, _ := provider.Find(in.ID)
			if in.Key == "" && old != nil {
				in.Key = old.Key
			}
			if old != nil {
				// the other keys are kept apart, in the Accounts list
				in.Keys = old.Keys
				if in.Key == old.Key {
					in.KeyName = old.KeyName
				}
			}
			if in.Icon == "" && old != nil && in.Preset == "" {
				in.Icon = old.Icon
			}
			if err := provider.Save(in); err != nil {
				fail(rw, err)
				return
			}
			// a new key means a new vendor list is worth a try; keep it short
			if p, err := provider.Find(in.ID); err == nil && p.Ready() && (old == nil || old.Key != p.Key) {
				ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
				p.Fetch(ctx)
				cancel()
			}
		case "key":
			// the saved key, for the editor's Show button; it never
			// leaves this machine (the panel is served on loopback)
			p, err := provider.Find(in.ID)
			if err != nil {
				fail(rw, err)
				return
			}
			writeJSON(rw, map[string]string{"key": p.Key})
			return
		case "delete":
			if err := provider.Delete(in.ID); err != nil {
				fail(rw, err)
				return
			}
		case "test":
			p, err := provider.Find(in.ID)
			if err != nil {
				fail(rw, err)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
			defer cancel()
			writeJSON(rw, struct {
				Results  []provider.Result `json:"results"`
				Provider providerJSON      `json:"provider"`
			}{p.Test(ctx), providerInfo(*p, agent.Detected())})
			return
		case "models":
			p, err := provider.Find(in.ID)
			if err != nil {
				fail(rw, err)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			defer cancel()
			var ms []catalog.Model
			if ms, err = p.Fetch(ctx); err != nil {
				fail(rw, err)
				return
			}
			writeJSON(rw, struct {
				Count    int          `json:"count"`
				Provider providerJSON `json:"provider"`
			}{len(ms), providerInfo(*p, agent.Detected())})
			return
		default:
			http.NotFound(rw, r)
			return
		}
		writeJSON(rw, providersState(gw))
	})
	// How much of its allowance each of an agent's accounts has used.
	mux.HandleFunc("GET /api/login/usage", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		writeJSON(rw, provider.LoginUsage(ctx, r.URL.Query().Get("agent")))
	})
	// Switching the account an agent is signed in to, among those magpie
	// remembers, and forgetting one.
	mux.HandleFunc("POST /api/login/{action}", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Agent, User string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		var err error
		switch r.PathValue("action") {
		case "switch":
			err = provider.SwitchLogin(in.Agent, in.User)
		case "forget":
			err = provider.ForgetLogin(in.Agent, in.User)
		case "on", "off":
			err = provider.SetLoginOn(in.Agent, in.User, r.PathValue("action") == "on")
		default:
			http.NotFound(rw, r)
			return
		}
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, providersState(gw))
	})
	// A provider's several keys: add one, put one in use, name or remove it.
	mux.HandleFunc("POST /api/keys/{action}", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ ID, Key, Name, Ref string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		var err error
		switch r.PathValue("action") {
		case "add":
			err = provider.AddKey(in.ID, in.Name, in.Key)
		case "use":
			err = provider.UseKey(in.ID, in.Ref)
		case "remove":
			err = provider.RemoveKey(in.ID, in.Ref)
		case "rename":
			err = provider.RenameKey(in.ID, in.Ref, in.Name)
		case "on", "off":
			err = provider.SetKeyOn(in.ID, in.Ref, r.PathValue("action") == "on")
		default:
			http.NotFound(rw, r)
			return
		}
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, providersState(gw))
	})
	// Adding a subscription: magpie opens the vendor's sign-in in the
	// browser and the window follows it until the account is in.
	mux.HandleFunc("POST /api/signin", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Agent string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		st, err := provider.StartSignIn(in.Agent)
		if err != nil {
			fail(rw, err)
			return
		}
		w.OpenURL(st.URL)
		writeJSON(rw, st)
	})
	mux.HandleFunc("GET /api/signin/{id}", func(rw http.ResponseWriter, r *http.Request) {
		st, ok := provider.SignInStatus(r.PathValue("id"))
		if !ok {
			http.NotFound(rw, r)
			return
		}
		writeJSON(rw, st)
	})
	mux.HandleFunc("POST /api/signin/{id}/cancel", func(rw http.ResponseWriter, r *http.Request) {
		provider.CancelSignIn(r.PathValue("id"))
		rw.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/open", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ URL string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if strings.HasPrefix(in.URL, "https://") || strings.HasPrefix(in.URL, "http://") {
			w.OpenURL(in.URL)
		}
		rw.WriteHeader(http.StatusNoContent)
	})
}
