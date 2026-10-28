package provider

import "testing"

func TestParseCursorModels(t *testing.T) {
	out := "\x1b[2K\x1b[GAvailable models\n\nauto - Auto (default)\ngpt-5.3-codex-high - Codex 5.3 High\nclaude-opus-5-thinking-high - Claude Opus 5 Thinking High (current)\n\nTip: use --model <id>\n"
	ms := parseCursorModels(out)
	want := [][2]string{{"auto", "Auto"}, {"gpt-5.3-codex-high", "Codex 5.3 High"}, {"claude-opus-5-thinking-high", "Claude Opus 5 Thinking High"}}
	if len(ms) != len(want) {
		t.Fatalf("got %d models: %+v", len(ms), ms)
	}
	for i, w := range want {
		if ms[i].ID != w[0] || ms[i].Name != w[1] {
			t.Errorf("model %d = %q %q, want %q %q", i, ms[i].ID, ms[i].Name, w[0], w[1])
		}
	}
}
