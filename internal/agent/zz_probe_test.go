package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// TEMPORARY probe: behavioural dump for baseline comparison. Delete before finishing.

func zzFieldLine(sb *strings.Builder, a *Agent, key string) {
	f := a.Field(key)
	if f == nil {
		fmt.Fprintf(sb, "  Field(%q) = nil\n", key)
		return
	}
	fmt.Fprintf(sb, "  Field(%q) -> Key=%q Label=%q Get=%q\n", key, f.Key, f.Label, f.Get())
}

func zzSetup(t *testing.T) {
	// fixed HOME so printed paths match on both trees; empty config dirs
	t.Setenv("HOME", "/probe-home-fixed")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", "/probe-home-fixed/.codex")
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: "http://127.0.0.1:1/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// structure: agents, fields, Get values, Field lookups
func TestZZProbeA(t *testing.T) {
	zzSetup(t)
	var sb strings.Builder
	for _, a := range All() {
		fmt.Fprintf(&sb, "AGENT %s name=%q path=%q dir=%q bin=%q aliases=%v nfields=%d\n",
			a.ID, a.Name, a.Path, a.Dir, a.Bin, a.Aliases, len(a.Fields))
		for _, f := range a.Fields {
			fmt.Fprintf(&sb, " field key=%q label=%q get=%q\n", f.Key, f.Label, f.Get())
		}
		for _, k := range []string{"provider", "model", "auth", "Model", "effort", "small", "thinking"} {
			zzFieldLine(&sb, a, k)
		}
	}
	zzEmit(t, "AGENT-A "+sb.String())
}

// options: value, label, group of every option list
func TestZZProbeB(t *testing.T) {
	zzSetup(t)
	var sb strings.Builder
	for _, a := range All() {
		fmt.Fprintf(&sb, "AGENT %s\n", a.ID)
		for _, f := range a.Fields {
			if f.Options == nil {
				continue
			}
			fmt.Fprintf(&sb, " opts %s:", f.Key)
			for _, o := range f.Options(map[string]string{}) {
				fmt.Fprintf(&sb, " [%s|%s|%s]", o.Value, o.Label, o.Group)
			}
			fmt.Fprintf(&sb, "\n")
		}
	}
	zzEmit(t, "AGENT-B "+sb.String())
}

// zzEmit reports the dump through assert annotations: one line per chunk,
// quotes and backslashes removed so the remote log keeps each chunk whole.
func zzEmit(t *testing.T, s string) {
	s = strings.NewReplacer("\"", "'", "\\", "/", "\n", " | ", "\t", " ").Replace(s)
	const max = 800
	for n := 0; len(s) > 0; n++ {
		c := s
		if len(c) > max {
			c = s[:max]
		}
		s = s[len(c):]
		t.Errorf("PROBE%02d %s", n, c)
	}
}
