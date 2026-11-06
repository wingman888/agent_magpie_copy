package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// geminiProto is the Gemini API as a client sees it. No upstream speaks it,
// so a Gemini request is always translated.
const geminiProto provider.Protocol = "gemini"

// geminiMethods are the methods dial serves for each model.
func geminiMethods() []string {
	return []string{"generateContent", "streamGenerateContent", "countTokens"}
}

// geminiModelObject describes a catalog model in the Gemini models shape.
func geminiModelObject(e provider.Entry) map[string]any {
	return map[string]any{
		"name":                       "models/" + e.ID,
		"version":                    Version,
		"displayName":                e.Name,
		"description":                e.Name + " via dial (" + e.Provider.Name + ")",
		"supportedGenerationMethods": geminiMethods(),
	}
}

// geminiModels lists every catalog model.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	list := []map[string]any{}
	for _, e := range provider.Catalog() {
		list = append(list, geminiModelObject(e))
	}
	writeJSON(w, 200, map[string]any{"models": list})
}

// geminiModel describes one catalog model, named "models/<id>" or "<id>".
func (s *Server) geminiModel(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.PathValue("id"), "models/")
	for _, e := range provider.Catalog() {
		if e.ID == id {
			writeJSON(w, 200, geminiModelObject(e))
			return
		}
	}
	geminiWriteError(w, http.StatusNotFound, unknownModel(id))
}

// geminiAPI routes POST /v1beta/models/{model}:{method}. The model id may
// itself contain colons, so the method is whatever follows the last one.
func (s *Server) geminiAPI(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("rest")
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		geminiWriteError(w, http.StatusNotFound, "expected /v1beta/models/{model}:{method}; methods are "+strings.Join(geminiMethods(), ", "))
		return
	}
	model, method := rest[:i], rest[i+1:]
	switch method {
	case "generateContent", "streamGenerateContent":
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
		if err != nil {
			geminiWriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.serve(w, r, geminiProto, body, model, method)
	case "countTokens":
		s.geminiCountTokens(w, r, model)
	default:
		geminiWriteError(w, http.StatusNotFound, "dial has no Gemini method "+method+"; methods are "+strings.Join(geminiMethods(), ", "))
	}
}

// geminiCountTokens estimates the tokens of a request, which may be given
// directly or wrapped in generateContentRequest. Only the rough size is
// known, so the count is the text length over four.
func (s *Server) geminiCountTokens(w http.ResponseWriter, r *http.Request, model string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		geminiWriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, _, ok := provider.Resolve(model); !ok {
		geminiWriteError(w, http.StatusNotFound, unknownModel(model))
		return
	}
	var wrap struct {
		GenerateContentRequest json.RawMessage `json:"generateContentRequest"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		geminiWriteError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if len(wrap.GenerateContentRequest) > 0 {
		body = wrap.GenerateContentRequest
	}
	req, err := parseGemini(body)
	if err != nil {
		geminiWriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	n := len(req.System)
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			n += len(p.Text) + len(p.Args) + len(p.Name)
		}
	}
	for _, t := range req.Tools {
		n += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	total := n / 4
	if total < 1 {
		total = 1
	}
	writeJSON(w, 200, map[string]any{"totalTokens": total})
}
