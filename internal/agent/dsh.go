package agent

// DeepSeek Harness (dsh) boots from its shipped rows and then applies one
// personal patch list, ~/.dsh/config.yaml: a YAML list of {id, config}
// entries, each replacing the whole config of the row with that id. magpie
// points dsh at the gateway with such entries: llm-deepseek (endpoint, key,
// the catalog as its model list), agent-loop (the TUI's main agent) and
// api-gateway (the model `dsh -p` and `dsh web` start sessions on; the TUI
// has no such row and only notes that). All are marked as magpie's, the
// user's own entries stay as they are, and one magpie replaces is stashed
// and put back when it steps out.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

const dshMark = "# magpie"

// dshModels are the models dsh reaches on its own, as it ships them.
var dshModels = []Option{
	{Value: "deepseek-v4-pro", Label: "DeepSeek-V4-Pro", Icon: "deepseek-color", Group: "DeepSeek"},
	{Value: "deepseek-v4-flash", Label: "DeepSeek-V4-Flash", Icon: "deepseek-color", Group: "DeepSeek"},
}

func dsh(home string) *Agent {
	dir := os.Getenv("DSH_HOME")
	if dir == "" {
		dir = filepath.Join(home, ".dsh")
	}
	path := filepath.Join(dir, "config.yaml")
	return &Agent{
		ID: "dsh", Name: "DeepSeek Harness", Icon: "deepseek-color", Aliases: []string{"deepseek-harness"},
		Bin: "dsh", Dir: dir, Path: path,
		Notice: func() string {
			var notes []string
			if Running(`(^|/)dsh( |$)`) {
				notes = append(notes, "dsh reads its config at start-up — restart open dsh sessions to use this.")
			}
			if dshSettingsEndpoint(filepath.Join(dir, "settings.yaml")) {
				notes = append(notes, "~/.dsh/settings.yaml sets its own DeepSeek endpoint or key, which dsh puts over magpie's; clear it in dsh's Models page to go through magpie.")
			}
			return strings.Join(notes, " ")
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string { return dshGet(path) },
			Set: func(v string) error { return dshSet(path, v) },
			Options: func(map[string]string) []Option {
				return append(append([]Option{}, dshModels...), viaMagpie(magpieID+"/")...)
			},
		}},
	}
}

// dshItem is one entry of the patch list, as its lines.
type dshItem struct {
	id     string
	magpie bool
	lines  []string
}

var dshIDLine = regexp.MustCompile(`^(?:- |  )id:\s*['"]?([^'"#\s]+)['"]?\s*(#.*)?$`)

// dshParse splits the patch list into what comes before its first entry and
// the entries. A file that is not a plain block list is left alone.
func dshParse(raw string) (head []string, items []dshItem, err error) {
	for _, l := range splitLinesKeep(raw) {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "- ") || l == "-":
			items = append(items, dshItem{lines: []string{l}})
		case len(items) == 0:
			if t != "" && !strings.HasPrefix(t, "#") && t != "[]" {
				return nil, nil, fmt.Errorf("%s is not a list of entries magpie can edit", "config.yaml")
			}
			if t != "[]" {
				head = append(head, l)
			}
			continue
		case t != "" && !strings.HasPrefix(t, "#") && !strings.HasPrefix(l, " "):
			return nil, nil, fmt.Errorf("%s is not a list of entries magpie can edit", "config.yaml")
		default:
			items[len(items)-1].lines = append(items[len(items)-1].lines, l)
		}
		it := &items[len(items)-1]
		if m := dshIDLine.FindStringSubmatch(l); m != nil && it.id == "" {
			it.id = m[1]
			it.magpie = strings.TrimSpace(m[2]) == dshMark
		}
	}
	return head, items, nil
}

func splitLinesKeep(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func dshRead(path string) ([]string, []dshItem, error) {
	b, err := edit.Read(path)
	if err != nil {
		return nil, nil, err
	}
	return dshParse(string(b))
}

func dshFind(items []dshItem, id string) int {
	for i, it := range items {
		if it.id == id {
			return i
		}
	}
	return -1
}

var dshModelLine = regexp.MustCompile(`^\s+model:\s*(.+?)\s*$`)

func dshGet(path string) string {
	_, items, err := dshRead(path)
	if err != nil {
		return ""
	}
	i := dshFind(items, "agent-loop")
	if i < 0 {
		return ""
	}
	model := ""
	for _, l := range items[i].lines {
		if m := dshModelLine.FindStringSubmatch(l); m != nil {
			model = yamlScalar(m[1])
			break
		}
	}
	if model == "" {
		return ""
	}
	if j := dshFind(items, "llm-deepseek"); j >= 0 && items[j].magpie {
		return magpieID + "/" + model
	}
	return model
}

// yamlScalar reads a plain or quoted YAML scalar.
func yamlScalar(v string) string {
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	if strings.HasPrefix(v, `"`) {
		var s string
		if json.Unmarshal([]byte(v), &s) == nil {
			return s
		}
	}
	return strings.Trim(v, `'"`)
}

func dshStashKey(path, id string) string { return "dsh:" + path + ":" + id }

// dshSet writes v as the main agent's model: a catalog model through the
// gateway, one of dsh's own models directly, or "" for dsh's own default.
func dshSet(path, v string) error {
	head, items, err := dshRead(path)
	if err != nil {
		return err
	}
	ref, viaGateway := strings.CutPrefix(v, magpieID+"/")
	if viaGateway && !isMagpie(ref) {
		return fmt.Errorf("unknown model %q", v)
	}
	put := func(id string, lines []string) {
		i := dshFind(items, id)
		if i >= 0 && !items[i].magpie {
			stash(map[string]string{dshStashKey(path, id): strings.Join(items[i].lines, "\n")})
		}
		it := dshItem{id: id, magpie: true, lines: lines}
		if i >= 0 {
			items[i] = it
		} else {
			items = append(items, it)
		}
	}
	drop := func(id string) {
		i := dshFind(items, id)
		if i < 0 || !items[i].magpie {
			return
		}
		if old := unstash(dshStashKey(path, id)); old != "" {
			items[i] = dshItem{id: id, lines: strings.Split(old, "\n")}
			return
		}
		items = append(items[:i], items[i+1:]...)
	}

	switch {
	case v == "":
		drop("api-gateway")
		drop("agent-loop")
		drop("llm-deepseek")
	case viaGateway:
		put("llm-deepseek", dshProviderLines())
		put("agent-loop", dshLoopLines(ref))
		put("api-gateway", dshRouteLines(ref))
	default:
		drop("api-gateway")
		drop("llm-deepseek")
		put("agent-loop", dshLoopLines(v))
	}

	var out []string
	out = append(out, head...)
	for _, it := range items {
		out = append(out, it.lines...)
	}
	if len(items) == 0 && len(head) == 0 {
		if _, err := os.Stat(path); err != nil {
			return nil // nothing was there, nothing to write
		}
		out = append(out, "[]")
	}
	return edit.WriteAtomic(path, []byte(strings.Join(out, "\n")+"\n"))
}

func yamlQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// dshProviderLines is the llm-deepseek entry pointing dsh at the gateway,
// with the catalog as the models its /model offers.
func dshProviderLines() []string {
	lines := []string{
		"- id: llm-deepseek " + dshMark,
		"  config:",
		"    apiKey: " + yamlQuote(gateway.Token),
		"    baseURL: " + yamlQuote(gatewayV1()),
		"    thinking: enabled",
		"    reasoningEffort: high",
		"    models:",
	}
	ms := magpieModels()
	if len(ms) == 0 {
		lines[len(lines)-1] = "    models: []"
	}
	for _, m := range ms {
		lines = append(lines, "      - id: "+yamlQuote(m.ID), "        name: "+yamlQuote(m.Name))
	}
	return lines
}

// dshLoopLines is the agent-loop entry dsh ships, with model in place.
func dshLoopLines(model string) []string {
	return []string{
		"- id: agent-loop " + dshMark,
		"  config:",
		"    agents:",
		"      - id: main",
		"        provider: deepseek-official",
		"        model: " + yamlQuote(model),
		"        cwd: !!js process.cwd()",
	}
}

// dshRouteLines is the api-gateway entry: the route headless and web
// sessions start on.
func dshRouteLines(model string) []string {
	return []string{
		"- id: api-gateway " + dshMark,
		"  config:",
		"    provider: deepseek-official",
		"    model: " + yamlQuote(model),
	}
}

// dshSettingsEndpoint reports whether dsh's own settings carry an
// llm-deepseek section with an endpoint or key, which dsh puts over the
// patch list.
func dshSettingsEndpoint(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	in := false
	for _, l := range splitLinesKeep(string(b)) {
		if l != "" && !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "#") {
			in = strings.HasPrefix(l, "llm-deepseek:")
			continue
		}
		t := strings.TrimSpace(l)
		if in && (strings.HasPrefix(t, "baseURL:") || strings.HasPrefix(t, "apiKey:")) {
			return true
		}
	}
	return false
}
