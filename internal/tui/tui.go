// Package tui is magpie in the terminal: one row per agent, arrow keys to pick
// a field, enter to change it.
package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/profile"
)

// ---- styling ---------------------------------------------------------------

var (
	cAccent = lipgloss.AdaptiveColor{Light: "#4F46E5", Dark: "#A5B4FC"}
	cText   = lipgloss.AdaptiveColor{Light: "#1F2328", Dark: "#E6E6E6"}
	cMuted  = lipgloss.AdaptiveColor{Light: "#8B8F98", Dark: "#7C8290"}
	cFaint  = lipgloss.AdaptiveColor{Light: "#C4C7CE", Dark: "#4A4F5A"}
	cOK     = lipgloss.AdaptiveColor{Light: "#0F9D58", Dark: "#7EE2A8"}
	cBad    = lipgloss.AdaptiveColor{Light: "#D93025", Dark: "#FF8A80"}
	cPill   = lipgloss.AdaptiveColor{Light: "#EEF0FF", Dark: "#2C2E4A"}

	sTitle  = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sCrumb  = lipgloss.NewStyle().Foreground(cMuted)
	sText   = lipgloss.NewStyle().Foreground(cText)
	sMuted  = lipgloss.NewStyle().Foreground(cMuted)
	sFaint  = lipgloss.NewStyle().Foreground(cFaint)
	sName   = lipgloss.NewStyle().Bold(true).Foreground(cText)
	sNameOn = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sPill   = lipgloss.NewStyle().Foreground(cAccent).Background(cPill).Padding(0, 1)
	sValue  = lipgloss.NewStyle().Foreground(cText).Padding(0, 1)
	sOK     = lipgloss.NewStyle().Foreground(cOK)
	sBad    = lipgloss.NewStyle().Foreground(cBad)
	sKey    = lipgloss.NewStyle().Foreground(cText)
	sCursor = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
)

const pad = "  "

// ---- model -----------------------------------------------------------------

type mode int

const (
	modeList mode = iota
	modePick
	modeProfiles
	modeName
)

type picker struct {
	crumbs []string
	input  textinput.Model
	items  []agent.Option
	match  []int
	cursor int
	custom bool
	onPick func(string) tea.Cmd
	onDel  func(string) tea.Cmd
	empty  string
}

type model struct {
	agents  []*agent.Agent
	values  []map[string]string
	row     int
	col     int
	mode    mode
	pk      picker
	name    textinput.Model
	flash   string
	flashOK bool
	w, h    int
	syncing bool
}

type flashMsg struct {
	text string
	ok   bool
}

type syncedMsg struct{ err error }

// Run starts magpie in the terminal.
func Run() error {
	m := newModel()
	if len(m.agents) == 0 {
		return fmt.Errorf("no supported agents found on this machine")
	}
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func newModel() model {
	m := model{agents: agent.Detected()}
	m.reload()
	return m
}

func (m *model) reload() {
	m.values = make([]map[string]string, len(m.agents))
	for i, a := range m.agents {
		m.values[i] = a.Values()
	}
}

func (m model) Init() tea.Cmd {
	if catalog.Source() == "" {
		m.syncing = true
		return syncCmd
	}
	return nil
}

func syncCmd() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return syncedMsg{err: catalog.Sync(ctx)}
}

// ---- update ----------------------------------------------------------------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case flashMsg:
		m.flash, m.flashOK = msg.text, msg.ok
		m.reload()
		return m, nil
	case syncedMsg:
		m.syncing = false
		if msg.err != nil {
			m.flash, m.flashOK = "catalog sync failed: "+msg.err.Error(), false
		} else {
			m.flash, m.flashOK = "model catalog synced from models.dev", true
		}
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		switch m.mode {
		case modeList:
			return m.updateList(msg)
		case modePick, modeProfiles:
			return m.updatePicker(msg)
		case modeName:
			return m.updateName(msg)
		}
	}
	return m, nil
}

func (m model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.flash = ""
	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit
	case "j", "down":
		m.row = (m.row + 1) % len(m.agents)
		m.col = 0
	case "k", "up":
		m.row = (m.row + len(m.agents) - 1) % len(m.agents)
		m.col = 0
	case "l", "right", "tab":
		m.col = (m.col + 1) % len(m.agents[m.row].Fields)
	case "h", "left", "shift+tab":
		n := len(m.agents[m.row].Fields)
		m.col = (m.col + n - 1) % n
	case "enter", " ":
		m.openFieldPicker()
	case "r":
		m.reload()
		m.flash, m.flashOK = "reloaded", true
	case "S":
		if !m.syncing {
			m.syncing = true
			return m, syncCmd
		}
	case "s":
		m.mode = modeName
		m.name = newInput("profile name")
	case "p":
		m.openProfiles()
	}
	return m, nil
}

func (m *model) openFieldPicker() {
	a := m.agents[m.row]
	f := a.Fields[m.col]
	cur := m.values[m.row][f.Key]
	items := f.Options(m.values[m.row])
	items = withCurrent(items, cur)
	set := f.Set
	label := a.Name + " · " + f.Label
	m.pk = picker{
		crumbs: []string{a.Name, f.Label},
		input:  newInput("type to filter, or enter a custom value"),
		items:  items,
		custom: true,
		empty:  "no models known yet, type one",
		onPick: func(v string) tea.Cmd {
			return func() tea.Msg {
				if err := set(v); err != nil {
					return flashMsg{text: err.Error()}
				}
				return flashMsg{text: label + " → " + v, ok: true}
			}
		},
	}
	m.pk.refilter()
	m.mode = modePick
}

func (m *model) openProfiles() {
	ps, err := profile.Load()
	if err != nil {
		m.flash, m.flashOK = err.Error(), false
		return
	}
	var items []agent.Option
	for _, n := range profile.Names(ps) {
		items = append(items, agent.Option{Value: n, Note: profile.Summary(ps[n])})
	}
	m.pk = picker{
		crumbs: []string{"profiles"},
		input:  newInput("filter profiles"),
		items:  items,
		empty:  "no profiles yet · press s in the list to save one",
		onPick: func(n string) tea.Cmd {
			return func() tea.Msg {
				ps, err := profile.Load()
				if err != nil {
					return flashMsg{text: err.Error()}
				}
				changed, err := profile.Apply(ps[n])
				if err != nil {
					return flashMsg{text: err.Error()}
				}
				return flashMsg{text: fmt.Sprintf("profile %s applied · %d change%s", n, changed, plural(changed)), ok: true}
			}
		},
		onDel: func(n string) tea.Cmd {
			return func() tea.Msg {
				if err := profile.Delete(n); err != nil {
					return flashMsg{text: err.Error()}
				}
				return flashMsg{text: "profile " + n + " deleted", ok: true}
			}
		},
	}
	m.pk.refilter()
	m.mode = modeProfiles
}

func (m model) updatePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		return m, nil
	case "down", "ctrl+n":
		if len(m.pk.match) > 0 {
			m.pk.cursor = (m.pk.cursor + 1) % len(m.pk.match)
		}
		return m, nil
	case "up", "ctrl+p":
		if len(m.pk.match) > 0 {
			m.pk.cursor = (m.pk.cursor + len(m.pk.match) - 1) % len(m.pk.match)
		}
		return m, nil
	case "ctrl+d":
		if m.mode == modeProfiles && m.pk.onDel != nil && len(m.pk.match) > 0 {
			n := m.pk.items[m.pk.match[m.pk.cursor]].Value
			m.mode = modeList
			return m, m.pk.onDel(n)
		}
		return m, nil
	case "enter":
		v, ok := m.pk.choice()
		if !ok {
			return m, nil
		}
		m.mode = modeList
		return m, m.pk.onPick(v)
	}
	var cmd tea.Cmd
	m.pk.input, cmd = m.pk.input.Update(msg)
	m.pk.refilter()
	return m, cmd
}

func (m model) updateName(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		return m, nil
	case "enter":
		n := strings.TrimSpace(m.name.Value())
		if n == "" {
			return m, nil
		}
		m.mode = modeList
		return m, func() tea.Msg {
			if err := profile.Save(n, profile.Snapshot()); err != nil {
				return flashMsg{text: err.Error()}
			}
			return flashMsg{text: "saved profile " + n, ok: true}
		}
	}
	var cmd tea.Cmd
	m.name, cmd = m.name.Update(msg)
	return m, cmd
}

// ---- picker logic ----------------------------------------------------------

func newInput(placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Prompt = ""
	ti.PlaceholderStyle = sFaint
	ti.TextStyle = sText
	ti.Cursor.Style = sCursor
	ti.CharLimit = 200
	ti.Focus()
	return ti
}

// withCurrent makes sure the current value is offered first, deduplicated.
func withCurrent(items []agent.Option, cur string) []agent.Option {
	seen := map[string]bool{}
	var out []agent.Option
	if cur != "" {
		seen[cur] = true
		note := "current"
		for _, it := range items {
			if it.Value == cur && it.Note != "" {
				note = it.Note + " · current"
			}
		}
		out = append(out, agent.Option{Value: cur, Note: note})
	}
	for _, it := range items {
		if seen[it.Value] {
			continue
		}
		seen[it.Value] = true
		out = append(out, it)
	}
	return out
}

type scored struct {
	idx, score int
}

func (p *picker) refilter() {
	q := strings.ToLower(strings.TrimSpace(p.input.Value()))
	p.match = p.match[:0]
	if q == "" {
		for i := range p.items {
			p.match = append(p.match, i)
		}
		p.cursor = clamp(p.cursor, len(p.match))
		return
	}
	var hits []scored
	for i, it := range p.items {
		if s, ok := score(strings.ToLower(it.Value), strings.ToLower(it.Note), q); ok {
			hits = append(hits, scored{i, s})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].score < hits[b].score })
	for _, h := range hits {
		p.match = append(p.match, h.idx)
	}
	p.cursor = 0
}

// score ranks prefix < substring < subsequence < note match; lower is better.
func score(value, note, q string) (int, bool) {
	if strings.HasPrefix(value, q) {
		return 0, true
	}
	if i := strings.Index(value, q); i >= 0 {
		return 1 + i, true
	}
	if subseq(value, q) {
		return 1000, true
	}
	if strings.Contains(note, q) {
		return 2000, true
	}
	return 0, false
}

func subseq(s, q string) bool {
	j := 0
	for i := 0; i < len(s) && j < len(q); i++ {
		if s[i] == q[j] {
			j++
		}
	}
	return j == len(q)
}

func (p *picker) choice() (string, bool) {
	if len(p.match) > 0 {
		return p.items[p.match[p.cursor]].Value, true
	}
	if v := strings.TrimSpace(p.input.Value()); p.custom && v != "" {
		return v, true
	}
	return "", false
}

func clamp(i, n int) int {
	if n == 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	if i < 0 {
		return 0
	}
	return i
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ---- view ------------------------------------------------------------------

func (m model) View() string {
	var body string
	var footer string
	switch m.mode {
	case modeList:
		body = m.viewList()
		footer = hints("↑↓", "agent", "←→", "field", "↵", "change", "s", "save profile", "p", "profiles", "q", "quit")
	case modePick, modeProfiles:
		body = m.viewPicker()
		if m.mode == modeProfiles {
			footer = hints("↑↓", "move", "↵", "apply", "ctrl+d", "delete", "esc", "back")
		} else {
			footer = hints("↑↓", "move", "↵", "select", "esc", "back")
		}
	case modeName:
		body = m.viewName()
		footer = hints("↵", "save", "esc", "cancel")
	}

	status := ""
	switch {
	case m.flash != "" && m.flashOK:
		status = sOK.Render("✓ ") + sText.Render(m.flash)
	case m.flash != "":
		status = sBad.Render("✗ ") + sText.Render(m.flash)
	case m.syncing:
		status = sMuted.Render("… syncing model catalog")
	}

	lines := strings.Count(body, "\n") + 1
	fill := m.h - lines - 3
	if fill < 1 {
		fill = 1
	}
	return body + strings.Repeat("\n", fill) + pad + status + "\n" + pad + footer + "\n"
}

func (m model) header(crumbs ...string) string {
	s := pad + sTitle.Render("◉ magpie")
	for _, c := range crumbs {
		s += sCrumb.Render(" › ") + sText.Render(c)
	}
	return s
}

func (m model) viewList() string {
	nameW := 0
	for _, a := range m.agents {
		nameW = max(nameW, lipgloss.Width(a.Name))
	}
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteString("\n\n")
	for i, a := range m.agents {
		sel := i == m.row
		marker := "  "
		name := sName.Render(padRight(a.Name, nameW))
		if sel {
			marker = sCursor.Render("▸ ")
			name = sNameOn.Render(padRight(a.Name, nameW))
		}
		line := pad + marker + name + "  "
		for j, f := range a.Fields {
			v := m.values[i][f.Key]
			var cell string
			switch {
			case sel && j == m.col:
				cell = sPill.Render(dash(v))
			case v == "":
				cell = sFaint.Render(" — ")
			default:
				cell = sValue.Render(v)
			}
			if j > 0 || f.Label != "model" {
				line += sMuted.Render(" " + f.Label)
			}
			line += cell + " "
		}
		if sel {
			p := tilde(a.Path)
			if room := m.w - lipgloss.Width(line) - lipgloss.Width(p) - 2; room > 0 {
				line += strings.Repeat(" ", room) + sFaint.Render(p)
			}
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m model) viewPicker() string {
	p := m.pk
	var b strings.Builder
	b.WriteString(m.header(p.crumbs...))
	b.WriteString("\n\n")
	b.WriteString(pad + sCursor.Render("❯ ") + p.input.View())
	b.WriteString("\n\n")

	if len(p.match) == 0 {
		if v, ok := p.choice(); ok {
			b.WriteString(pad + "  " + sMuted.Render("↵ use ") + sText.Render(v) + sMuted.Render(" as a custom value"))
		} else if len(p.items) == 0 {
			b.WriteString(pad + "  " + sMuted.Render(p.empty))
		} else {
			b.WriteString(pad + "  " + sMuted.Render("no match"))
		}
		return b.String()
	}

	valW := 0
	for _, i := range p.match {
		valW = max(valW, lipgloss.Width(p.items[i].Value))
	}
	valW = min(valW, max(24, m.w/2))

	visible := max(3, m.h-8)
	start := 0
	if p.cursor >= visible {
		start = p.cursor - visible + 1
	}
	end := min(len(p.match), start+visible)
	for k := start; k < end; k++ {
		it := p.items[p.match[k]]
		marker, val := "  ", sText.Render(padRight(it.Value, valW))
		if k == p.cursor {
			marker, val = sCursor.Render("▸ "), sNameOn.Render(padRight(it.Value, valW))
		}
		line := pad + marker + val
		if it.Note != "" {
			line += "  " + sMuted.Render(it.Note)
		}
		b.WriteString(line)
		if k < end-1 {
			b.WriteString("\n")
		}
	}
	if end < len(p.match) || start > 0 {
		b.WriteString("\n" + pad + "  " + sFaint.Render(fmt.Sprintf("%d–%d of %d", start+1, end, len(p.match))))
	}
	return b.String()
}

func (m model) viewName() string {
	var b strings.Builder
	b.WriteString(m.header("save profile"))
	b.WriteString("\n\n")
	b.WriteString(pad + sCursor.Render("❯ ") + m.name.View())
	b.WriteString("\n\n")
	b.WriteString(pad + "  " + sMuted.Render("snapshots every agent's current settings"))
	b.WriteString("\n\n")
	snap := profile.Snapshot()
	for _, k := range sortedKeys(snap) {
		b.WriteString(pad + "  " + sFaint.Render(padRight(k, 18)) + sText.Render(snap[k]) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func hints(kv ...string) string {
	var parts []string
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, sKey.Render(kv[i])+" "+sMuted.Render(kv[i+1]))
	}
	return strings.Join(parts, sFaint.Render("  ·  "))
}

func padRight(s string, w int) string {
	if n := w - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func dash(v string) string {
	if v == "" {
		return "—"
	}
	return v
}

func tilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return "~" + string(filepath.Separator) + rel
		}
	}
	return p
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
