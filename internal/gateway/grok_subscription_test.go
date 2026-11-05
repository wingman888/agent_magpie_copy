package gateway

import (
	"strings"
	"testing"
	"time"
)

func TestGrokEnvRunsInMagpiesHome(t *testing.T) {
	got := strings.Join(grokEnv([]string{"PATH=/bin", "HOME=/Users/me", "GROK_HOME=/x", "XAI_API_KEY=k", "GROK_API_KEY=k", "MAGPIE_MCP_CALLBACK=old", "KEEP=1"}, "/cache/home", "'magpie' grok-token"), "\n")
	for _, bad := range []string{"HOME=/Users/me", "GROK_HOME=", "XAI_API_KEY=", "GROK_API_KEY=", "MAGPIE_MCP_CALLBACK="} {
		if strings.Contains(got, bad) {
			t.Fatalf("kept %s in %q", bad, got)
		}
	}
	for _, want := range []string{"PATH=/bin", "KEEP=1", "HOME=/cache/home", "GROK_AUTH_PROVIDER_COMMAND='magpie' grok-token"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %q", want, got)
		}
	}
}

func TestGrokConfigKeepsOnlyTheCallersTools(t *testing.T) {
	cfg := grokConfig(`/Applications/magpie.app/Contents/MacOS/magpie`)
	for _, want := range []string{
		"[mcp_servers.magpie]\ncommand = \"/Applications/magpie.app/Contents/MacOS/magpie\"\nargs = [\"claude-mcp-helper\"]",
		"tool_timeout_sec = 86400",
		"[compat.claude]\nmcps = false\nskills = false\nhooks = false",
		"[compat.cursor]\nmcps = false",
		"auto_update = false",
		`allow = ["MCPTool(magpie__*)"]`,
	} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("config lacks %q:\n%s", want, cfg)
		}
	}
}

func TestGrokAuthCommandQuotes(t *testing.T) {
	got := grokAuthCommand("/Apps/it's magpie", "/Users/me/.grok", "/Users/me/.grok/bin/grok")
	if got != `'/Apps/it'\''s magpie' grok-token '/Users/me/.grok' '/Users/me/.grok/bin/grok'` {
		t.Fatalf("got %s", got)
	}
}

func TestRenderGrokPromptNamesTools(t *testing.T) {
	req := &Request{Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "weather?"}}}}}
	got := renderGrokPrompt(req, []bridgeTool{{Name: "get_weather"}, {Name: "read"}})
	if !strings.Contains(got, "magpie__get_weather, magpie__read") || !strings.Contains(got, "use_tool") || !strings.Contains(got, "weather?") {
		t.Fatalf("prompt = %q", got)
	}
	if strings.Contains(renderGrokPrompt(req, nil), "external_system_instructions") {
		t.Fatal("no tools, yet told of them")
	}
}

func TestReadGrokStream(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","tools":["search_tool","use_tool"]}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_0"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"po"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ng"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","name":"use_tool","input":{}}}}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"pong"}]}}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_1"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"whole"}]}}`,
		`{"type":"stream_event","parent_tool_use_id":"x","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"sub"}}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"pong","usage":{"input_tokens":262,"output_tokens":27,"cache_read_input_tokens":28416,"cache_creation_input_tokens":0}}`,
	}, "\n")
	run := &subscriptionRun{pending: map[string]chan mcpToolResult{}}
	run.timer = time.AfterFunc(time.Hour, func() {})
	c := &cursorTurn{run: run}
	seg := run.attach()
	go c.readGrok(strings.NewReader(stream))
	var think, text string
	var usage Usage
	var stop string
	for ev := range seg {
		switch ev.Kind {
		case KThink:
			think += ev.Text + "|"
		case KText:
			text += ev.Text
		case KUsage:
			usage = ev.Usage
		case KStop:
			stop = ev.Stop
		case KError:
			t.Fatalf("error %q", ev.Text)
		}
	}
	if think != "hmm|whole|" || text != "pong" || stop != "stop" {
		t.Fatalf("think=%q text=%q stop=%q", think, text, stop)
	}
	if usage.Input != 262 || usage.CacheRead != 28416 || usage.Output != 27 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestReadGrokError(t *testing.T) {
	run := &subscriptionRun{pending: map[string]chan mcpToolResult{}}
	c := &cursorTurn{run: run}
	seg := run.attach()
	go c.readGrok(strings.NewReader(`{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["rate limited"]}`))
	var got string
	for ev := range seg {
		if ev.Kind == KError {
			got = ev.Text
		}
	}
	if got != "rate limited" {
		t.Fatalf("error = %q", got)
	}
}
