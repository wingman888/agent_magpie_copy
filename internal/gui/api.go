// Package gui hosts the desktop app: a tray panel and a regular window that
// share one small web UI. The UI talks to Go over a tiny JSON API served by
// the same handler that serves the static assets, so no binding generator or
// bundler is involved.
package gui

import (
	"context"
	"embed"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Version is the build's version string, shown in Settings.
var Version = "dev"

//go:embed assets
var assets embed.FS

// Windows is what the API needs from the host application.
type Windows interface {
	HidePanel()
	// ShowMain brings the window up, on the named tab when view is set.
	ShowMain(view string)
	Quit()
	// OpenURL hands a link to the system browser.
	OpenURL(url string)
	// FitPanel asks for the panel to be tall enough for its content.
	FitPanel(height int)
}

type fieldJSON struct {
	Key     string         `json:"key"`
	Label   string         `json:"label"`
	Value   string         `json:"value"`
	Options []agent.Option `json:"options"`
}

type agentJSON struct {
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	Icon   string      `json:"icon"`
	Path   string      `json:"path"`
	Fields []fieldJSON `json:"fields"`
}

type profileJSON struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

type stateJSON struct {
	Agents   []agentJSON       `json:"agents"`
	Profiles []profileJSON     `json:"profiles"`
	Catalog  string            `json:"catalog"`
	Notice   string            `json:"notice,omitempty"` // advice after a change, e.g. "restart Codex"
	Settings settings.Settings `json:"settings"`
}

// settingsJSON is the Settings page: the two choices plus the facts it shows.
type settingsJSON struct {
	settings.Settings
	Version string `json:"version"`
	Dir     string `json:"dir"`     // where magpie keeps its files, as shown
	Path    string `json:"path"`    // the same, absolute, for opening it
	Gateway string `json:"gateway"` // the local endpoint
}

func settingsState() settingsJSON {
	return settingsJSON{Settings: settings.Load(), Version: Version, Dir: tilde(settings.Dir()), Path: settings.Dir(), Gateway: gateway.URL()}
}

// Handler serves the embedded UI and the JSON API.
// gw is the gateway this process serves, or nil when another magpie has it.
func Handler(w Windows, gw *gateway.Server) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", devPage(http.FileServer(http.FS(staticFS()))))
	devRoutes(mux)
	mux.HandleFunc("GET /api/state", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, state())
	})
	mux.HandleFunc("POST /api/set", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Agent, Field, Value string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		a, err := agent.Find(in.Agent)
		if err != nil {
			fail(rw, err)
			return
		}
		f := a.Field(in.Field)
		if f == nil {
			http.Error(rw, "unknown field", http.StatusBadRequest)
			return
		}
		if err := f.Set(strings.TrimSpace(in.Value)); err != nil {
			fail(rw, err)
			return
		}
		s := state()
		if a.Notice != nil {
			s.Notice = a.Notice()
		}
		writeJSON(rw, s)
	})
	mux.HandleFunc("POST /api/profile/{action}", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Name string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		in.Name = strings.TrimSpace(in.Name)
		var err error
		changed := 0
		switch r.PathValue("action") {
		case "save":
			err = profile.Save(in.Name, profile.Snapshot())
		case "use":
			var ps map[string]profile.Profile
			if ps, err = profile.Load(); err == nil {
				changed, err = profile.Apply(ps[in.Name])
			}
		case "delete":
			err = profile.Delete(in.Name)
		default:
			http.NotFound(rw, r)
			return
		}
		if err != nil {
			fail(rw, err)
			return
		}
		s := state()
		writeJSON(rw, struct {
			stateJSON
			Changed int `json:"changed"`
		}{s, changed})
	})
	mux.HandleFunc("POST /api/sync", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := catalog.Sync(ctx); err != nil {
			fail(rw, err)
			return
		}
		for _, p := range provider.All() {
			if p.Ready() {
				c, cancel := context.WithTimeout(ctx, 8*time.Second)
				p.Fetch(c)
				cancel()
			}
		}
		writeJSON(rw, state())
	})
	providerRoutes(mux, w, gw)
	usageRoutes(mux)
	updateRoutes(mux, w)
	mux.HandleFunc("GET /api/settings", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, settingsState())
	})
	mux.HandleFunc("POST /api/settings", func(rw http.ResponseWriter, r *http.Request) {
		var in settings.Settings
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := settings.Save(in); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, settingsState())
	})
	mux.HandleFunc("POST /api/window/{action}", func(rw http.ResponseWriter, r *http.Request) {
		switch r.PathValue("action") {
		case "hide":
			w.HidePanel()
		case "main":
			w.ShowMain(r.URL.Query().Get("view"))
		case "quit":
			w.Quit()
		case "fit":
			if h, err := strconv.Atoi(r.URL.Query().Get("h")); err == nil {
				w.FitPanel(h)
			}
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	devListen(mux)
	return mux
}

func state() stateJSON {
	s := stateJSON{Agents: []agentJSON{}, Profiles: []profileJSON{}, Catalog: catalog.Source(), Settings: settings.Load()}
	for _, a := range agent.Detected() {
		vals := a.Values()
		aj := agentJSON{ID: a.ID, Name: a.Name, Icon: a.Icon, Path: tilde(a.Path)}
		for _, f := range a.Fields {
			opts := f.Options(vals)
			if opts == nil {
				opts = []agent.Option{}
			}
			aj.Fields = append(aj.Fields, fieldJSON{Key: f.Key, Label: f.Label, Value: vals[f.Key], Options: opts})
		}
		s.Agents = append(s.Agents, aj)
	}
	if ps, err := profile.Load(); err == nil {
		for _, n := range profile.Names(ps) {
			s.Profiles = append(s.Profiles, profileJSON{Name: n, Summary: profile.Summary(ps[n])})
		}
	}
	return s
}

func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(v)
}

func fail(rw http.ResponseWriter, err error) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(rw).Encode(map[string]string{"error": err.Error()})
}

func tilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}
