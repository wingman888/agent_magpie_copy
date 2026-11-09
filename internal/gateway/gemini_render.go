package gateway

import (
	"encoding/json"
	"strings"
)

// ---- Gemini replies -----------------------------------------------------------

// stopToGemini maps the internal stop reason onto Gemini's finishReason. A
// reply that calls functions still finishes with STOP, as Gemini does.
func stopToGemini(s string) string {
	switch s {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// geminiUsage is usageMetadata. promptTokenCount includes cached tokens, as
// it does in the upstream usage figures.
func geminiUsage(u Usage) map[string]any {
	prompt := u.Input + u.CacheRead
	m := map[string]any{
		"promptTokenCount":     prompt,
		"candidatesTokenCount": u.Output,
		"thoughtsTokenCount":   u.Reasoning,
		"totalTokenCount":      prompt + u.Output + u.Reasoning,
	}
	if u.CacheRead > 0 {
		m["cachedContentTokenCount"] = u.CacheRead
	}
	return m
}

// geminiParts renders reply parts as Gemini parts.
func geminiParts(parts []Part) []map[string]any {
	out := []map[string]any{}
	for _, p := range parts {
		switch p.Kind {
		case Text:
			if p.Text != "" {
				out = append(out, map[string]any{"text": p.Text})
			}
		case Thinking:
			if p.Text != "" {
				out = append(out, map[string]any{"text": p.Text, "thought": true})
			}
		case ToolCall:
			id := p.ID
			if id == "" {
				id = "call_" + newID()
			}
			out = append(out, map[string]any{"functionCall": map[string]any{"id": id, "name": p.Name, "args": argsOf(p)}})
		case Image:
			if p.Data != "" {
				out = append(out, map[string]any{"inlineData": map[string]any{"mimeType": p.MediaType, "data": p.Data}})
			} else if p.URL != "" {
				out = append(out, map[string]any{"fileData": map[string]any{"mimeType": p.MediaType, "fileUri": p.URL}})
			}
		}
	}
	return out
}

// renderGemini is a whole reply in Gemini's format, compact JSON.
func renderGemini(res Result, model string) []byte {
	if res.Model != "" {
		model = res.Model
	}
	id := res.ID
	if id == "" {
		id = "resp_" + newID()
	}
	b, _ := json.Marshal(map[string]any{
		"candidates": []map[string]any{{
			"content":      map[string]any{"role": "model", "parts": geminiParts(res.Parts)},
			"finishReason": stopToGemini(res.Stop),
			"index":        0,
		}},
		"usageMetadata": geminiUsage(res.Usage),
		"modelVersion":  model,
		"responseId":    id,
	})
	return b
}

// renderGeminiError is an error body in Gemini's style.
func renderGeminiError(code int, status, msg string) []byte {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": msg, "status": status}})
	return b
}

// geminiCandidate wraps parts in a candidate's content.
func geminiCandidate(parts []map[string]any) map[string]any {
	return map[string]any{"content": map[string]any{"role": "model", "parts": parts}}
}

// geminiEncoder writes a reply as a Gemini stream: one SSE event per part, in
// the order the parts arrived. The last part is held back until the stream
// ends so that it can carry finishReason and usageMetadata. A reply with no
// parts gets a final event with an empty parts list.
type geminiEncoder struct {
	w     *sseWriter
	model string
	col   collector

	held    map[string]any // the part waiting for the end of the stream
	hasHeld bool

	toolOpen bool // a function call is being streamed
	toolID   string
	toolName string
	toolArgs strings.Builder
}

// emit queues a part, sending the one before it.
func (e *geminiEncoder) emit(part map[string]any) {
	if e.hasHeld {
		e.w.event("", map[string]any{"candidates": []map[string]any{geminiCandidate([]map[string]any{e.held})}})
	}
	e.held, e.hasHeld = part, true
}

// flushTool completes the open function call, if any, as one part.
func (e *geminiEncoder) flushTool() {
	if !e.toolOpen {
		return
	}
	e.toolOpen = false
	id := e.toolID
	if id == "" {
		id = "call_" + newID()
	}
	e.emit(map[string]any{"functionCall": map[string]any{"id": id, "name": e.toolName, "args": parseArgs(e.toolArgs.String())}})
	e.toolArgs.Reset()
}

func (e *geminiEncoder) event(ev Event) {
	switch ev.Kind {
	case KStart:
		if ev.Model != "" {
			e.model = ev.Model
		}
	case KText:
		e.flushTool()
		if ev.Text != "" {
			e.emit(map[string]any{"text": ev.Text})
		}
	case KThink:
		e.flushTool()
		if ev.Text != "" {
			e.emit(map[string]any{"text": ev.Text, "thought": true})
		}
	case KToolStart:
		e.flushTool()
		e.toolOpen, e.toolID, e.toolName = true, ev.ID, ev.Name
		e.toolArgs.Reset()
	case KToolArgs:
		if e.toolOpen {
			e.toolArgs.WriteString(ev.Text)
		}
	case KError:
		e.flushTool()
		e.w.event("", map[string]any{"error": map[string]any{"code": 500, "message": ev.Text, "status": "INTERNAL"}})
	}
	e.col.add(ev)
}

func (e *geminiEncoder) finish() {
	e.flushTool()
	res := e.col.finish()
	model := e.model
	if res.Model != "" {
		model = res.Model
	}
	id := res.ID
	if id == "" {
		id = "resp_" + newID()
	}
	parts := []map[string]any{}
	if e.hasHeld {
		parts = append(parts, e.held)
		e.held, e.hasHeld = nil, false
	}
	cand := geminiCandidate(parts)
	cand["finishReason"] = stopToGemini(res.Stop)
	cand["index"] = 0
	e.w.event("", map[string]any{
		"candidates":    []map[string]any{cand},
		"usageMetadata": geminiUsage(res.Usage),
		"modelVersion":  model,
		"responseId":    id,
	})
}
