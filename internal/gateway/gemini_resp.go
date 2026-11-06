package gateway

import (
	"encoding/json"
)

// ---- the gateway's reply → Gemini's response shape -------------------------

// geminiPart is one part of a Gemini reply.
func geminiPart(p Part) map[string]any {
	switch p.Kind {
	case Text:
		return map[string]any{"text": p.Text}
	case Thinking:
		m := map[string]any{"text": p.Text, "thought": true}
		if p.Signature != "" {
			m["thoughtSignature"] = p.Signature
		}
		return m
	case ToolCall:
		id := p.ID
		if id == "" {
			id = "call_" + newID()
		}
		return map[string]any{"functionCall": map[string]any{"id": id, "name": p.Name, "args": json.RawMessage(argsOf(p))}}
	}
	return nil
}

// stopToGemini: tool calls end with STOP too, as Gemini reports them.
func stopToGemini(stop string) string {
	switch stop {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// usageGemini: the prompt count is everything the upstream read, cached or not.
func usageGemini(u Usage) map[string]any {
	in := u.Input + u.CacheRead + u.CacheWrite
	m := map[string]any{
		"promptTokenCount":     in,
		"candidatesTokenCount": u.Output,
		"totalTokenCount":      in + u.Output + u.Reasoning,
		"thoughtsTokenCount":   u.Reasoning,
	}
	if u.CacheRead > 0 {
		m["cachedContentTokenCount"] = u.CacheRead
	}
	return m
}

// geminiEnvelope is the response object: parts, finish reason and usage.
// A failed reply finishes with OTHER.
func geminiEnvelope(res Result, model string, parts []Part, failed bool) map[string]any {
	content := []map[string]any{}
	for _, p := range parts {
		if m := geminiPart(p); m != nil {
			content = append(content, m)
		}
	}
	finish := stopToGemini(res.Stop)
	if failed {
		finish = "OTHER"
	}
	if res.Model != "" {
		model = res.Model
	}
	id := res.ID
	if id == "" {
		id = "resp_" + newID()
	}
	return map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"role": "model", "parts": content},
			"finishReason": finish,
			"index":        0,
		}},
		"usageMetadata": usageGemini(res.Usage),
		"modelVersion":  model,
		"responseId":    id,
	}
}

// renderGemini is the non-streaming reply.
func renderGemini(res Result, model string, failed bool) []byte {
	b, _ := json.Marshal(geminiEnvelope(res, model, res.Parts, failed))
	return b
}

// geminiOut turns upstream events into Gemini output. A part is sent once the
// reply moves past it, so every streamed event carries one whole part.
type geminiOut struct {
	w      *sseWriter
	stream bool
	col    collector
	sent   int // parts already streamed
}

func (g *geminiOut) event(ev Event) {
	g.col.add(ev)
	if !g.stream {
		return
	}
	// the last part may still grow (text, arguments), so hold it back
	for ; g.sent < len(g.col.res.Parts)-1; g.sent++ {
		g.part(g.col.res.Parts[g.sent])
	}
}

// part sends one part as its own event.
func (g *geminiOut) part(p Part) {
	m := geminiPart(p)
	if m == nil {
		return
	}
	g.w.event("", map[string]any{"candidates": []any{map[string]any{
		"content": map[string]any{"role": "model", "parts": []any{m}},
	}}})
}
