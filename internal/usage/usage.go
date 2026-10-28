// Package usage keeps the token count of every call the gateway serves, so
// magpie can show what each agent and model consumed and roughly what it cost.
// Records go to one JSON-lines file next to providers.json; nothing leaves
// the machine.
package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Record is one call.
type Record struct {
	Time       time.Time `json:"t"`
	Agent      string    `json:"agent"` // magpie agent id, or the client's product name
	Provider   string    `json:"provider"`
	Model      string    `json:"model"` // the provider's model id
	Input      int       `json:"in"`
	Output     int       `json:"out"`
	CacheRead  int       `json:"cache_read,omitempty"`
	CacheWrite int       `json:"cache_write,omitempty"`
	Reasoning  int       `json:"reasoning,omitempty"`
	Millis     int64     `json:"ms"`
	Status     int       `json:"status"`
}

// Path is the log file: ~/.config/magpie/usage.jsonl (XDG-aware).
func Path() string { return filepath.Join(filepath.Dir(provider.Path()), "usage.jsonl") }

var mu sync.Mutex

// Append writes one record. Errors are swallowed: accounting must never
// break a call.
func Append(r Record) {
	if r.Time.IsZero() {
		r.Time = time.Now()
	}
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(Path(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

// Load reads every record since a time (zero means all), oldest first.
func Load(since time.Time) []Record {
	mu.Lock()
	defer mu.Unlock()
	f, err := os.Open(Path())
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r Record
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		if !since.IsZero() && r.Time.Before(since) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// AgentOf names the agent behind a client User-Agent. Known agents map to
// their magpie id; anything else keeps its product name.
func AgentOf(ua string) string {
	ua = strings.TrimSpace(ua)
	name, _, _ := strings.Cut(ua, "/")
	name, _, _ = strings.Cut(name, " ")
	l := strings.ToLower(name)
	for _, k := range known {
		if strings.HasPrefix(l, k.prefix) {
			return k.id
		}
	}
	if name == "" {
		return "other"
	}
	return name
}

var known = []struct{ prefix, id string }{
	{"claude-cli", "claude"}, {"claude-code", "claude"},
	{"codex", "codex"},
	{"geminicli", "gemini"}, {"gemini-cli", "gemini"},
	{"opencode", "opencode"},
	{"pi-", "pi"}, {"pi/", "pi"},
	{"goose", "goose"},
	{"cursor", "cursor"},
	{"copilot", "copilot"}, {"github-copilot", "copilot"},
	{"crush", "crush"},
}

// ---- summaries --------------------------------------------------------------

// Period is a window of time to sum over.
type Period string

const (
	Today Period = "today"
	Week  Period = "7d"
	Month Period = "30d"
	All   Period = "all"
)

// Periods lists the windows in order.
var Periods = []Period{Today, Week, Month, All}

// Totals is a sum of calls.
type Totals struct {
	Calls      int     `json:"calls"`
	Errors     int     `json:"errors"`
	Input      int     `json:"input"`
	Output     int     `json:"output"`
	CacheRead  int     `json:"cache_read"`
	CacheWrite int     `json:"cache_write"`
	Reasoning  int     `json:"reasoning"`
	Cost       float64 `json:"cost"`     // USD at list prices, for the priced calls
	Unpriced   int     `json:"unpriced"` // calls with tokens but no known price
}

// Tokens is what went in and out, excluding cache traffic.
func (t Totals) Tokens() int { return t.Input + t.Output }

func (t *Totals) add(r Record, price *catalog.Price) {
	t.Calls++
	if r.Status >= 400 {
		t.Errors++
	}
	t.Input += r.Input
	t.Output += r.Output
	t.CacheRead += r.CacheRead
	t.CacheWrite += r.CacheWrite
	t.Reasoning += r.Reasoning
	if r.Input+r.Output == 0 {
		return
	}
	if price == nil {
		t.Unpriced++
		return
	}
	t.Cost += price.Cost(r.Input, r.Output, r.CacheRead, r.CacheWrite)
}

// Group is the share of one agent or model.
type Group struct {
	ID       string `json:"id"`
	Provider string `json:"provider,omitempty"` // models only
	Model    string `json:"model,omitempty"`
	Totals
}

// Point is one bar of the timeline.
type Point struct {
	Label string    `json:"label"`
	Time  time.Time `json:"time"`
	Totals
}

// Summary is a period, summed up.
type Summary struct {
	Period Period    `json:"period"`
	Since  time.Time `json:"since"`
	Totals
	Bucket string  `json:"bucket"` // hour | day | week
	Series []Point `json:"series"`
	Agents []Group `json:"agents"`
	Models []Group `json:"models"`
}

// Summarize sums the log over a period, as of now.
func Summarize(p Period) Summary {
	return summarize(p, time.Now(), Load(time.Time{}))
}

func summarize(p Period, now time.Time, recs []Record) Summary {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	s := Summary{Period: p, Bucket: "day", Agents: []Group{}, Models: []Group{}, Series: []Point{}}
	var n int
	switch p {
	case Today:
		s.Since, s.Bucket = day, "hour"
	case Week:
		s.Since, n = day.AddDate(0, 0, -6), 7
	case Month:
		s.Since, n = day.AddDate(0, 0, -29), 30
	default:
		s.Period = All
		if len(recs) > 0 {
			first := recs[0].Time.In(now.Location())
			s.Since = time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, now.Location())
		} else {
			s.Since = day
		}
		if n = int(day.Sub(s.Since).Hours()/24) + 1; n > 60 {
			s.Bucket = "week"
			// start on the Monday of the first week
			off := (int(s.Since.Weekday()) + 6) % 7
			s.Since = s.Since.AddDate(0, 0, -off)
			n = int(day.Sub(s.Since).Hours()/(24*7)) + 1
		}
	}
	// the empty timeline, so quiet days still take their place
	switch s.Bucket {
	case "hour":
		for h := 0; h < 24; h++ {
			t := day.Add(time.Duration(h) * time.Hour)
			s.Series = append(s.Series, Point{Label: t.Format("15"), Time: t})
		}
	case "day":
		for i := 0; i < n; i++ {
			t := s.Since.AddDate(0, 0, i)
			s.Series = append(s.Series, Point{Label: t.Format("Jan 2"), Time: t})
		}
	case "week":
		for i := 0; i < n; i++ {
			t := s.Since.AddDate(0, 0, 7*i)
			s.Series = append(s.Series, Point{Label: t.Format("Jan 2"), Time: t})
		}
	}

	prices := map[string]*catalog.Price{}
	priceOf := func(r Record) *catalog.Price {
		k := r.Provider + "/" + r.Model
		if pr, ok := prices[k]; ok {
			return pr
		}
		var pr *catalog.Price
		for _, p := range provider.All() {
			if p.ID == r.Provider && p.Catalog != "" {
				if v, ok := catalog.PriceOf(p.Catalog, r.Model); ok {
					pr = &v
				}
				break
			}
		}
		prices[k] = pr
		return pr
	}
	agents := map[string]*Group{}
	models := map[string]*Group{}
	for _, r := range recs {
		t := r.Time.In(now.Location())
		if t.Before(s.Since) {
			continue
		}
		pr := priceOf(r)
		s.Totals.add(r, pr)
		var i int
		switch s.Bucket {
		case "hour":
			i = int(t.Sub(s.Since).Hours())
		case "day":
			i = int(t.Sub(s.Since).Hours() / 24)
		case "week":
			i = int(t.Sub(s.Since).Hours() / (24 * 7))
		}
		if i >= 0 && i < len(s.Series) {
			s.Series[i].add(r, pr)
		}
		a := agents[r.Agent]
		if a == nil {
			a = &Group{ID: r.Agent}
			agents[r.Agent] = a
		}
		a.add(r, pr)
		k := r.Provider + "/" + r.Model
		m := models[k]
		if m == nil {
			m = &Group{ID: k, Provider: r.Provider, Model: r.Model}
			models[k] = m
		}
		m.add(r, pr)
	}
	for _, g := range agents {
		s.Agents = append(s.Agents, *g)
	}
	for _, g := range models {
		s.Models = append(s.Models, *g)
	}
	byTokens := func(gs []Group) {
		sort.SliceStable(gs, func(i, j int) bool {
			if a, b := gs[i].Tokens(), gs[j].Tokens(); a != b {
				return a > b
			}
			if gs[i].Calls != gs[j].Calls {
				return gs[i].Calls > gs[j].Calls
			}
			return gs[i].ID < gs[j].ID
		})
	}
	byTokens(s.Agents)
	byTokens(s.Models)
	return s
}
