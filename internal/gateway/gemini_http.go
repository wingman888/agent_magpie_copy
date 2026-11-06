package gateway

// The Gemini API routes. Gemini puts the model and the method in the path,
// as "/v1beta/models/{model}:{method}", so the model is whatever comes before
// the last colon: catalog ids may themselves contain colons.

import (
	"io"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// geminiMethods are the methods dial serves on each model.
var geminiMethods = []string{"generateContent", "streamGenerateContent", "countTokens"}

// splitGeminiPath splits "fake/a:b:generateContent" into the model
// "fake/a:b" and the method "generateContent".
func splitGeminiPath(rest string) (model, method string, ok bool) {
	i := strings.LastIndex(rest, ":")
	if i <= 0 || i == len(rest)-1 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

// gemini routes POST /v1beta/models/{rest...}.
func (s *Server) gemini(w http.ResponseWriter, r *http.Request) {
	model, method, ok := splitGeminiPath(r.PathValue("rest"))
	if !ok {
		writeGeminiError(w, 404, "expected /v1beta/models/{model}:{method}")
		return
	}
	switch method {
	case "generateContent":
		s.geminiGenerate(w, r, model, false)
	case "streamGenerateContent":
		if r.URL.Query().Get("alt") != "sse" {
			writeGeminiError(w, 400, "streamGenerateContent is served as server-sent events; add ?alt=sse")
			return
		}
		s.geminiGenerate(w, r, model, true)
	case "countTokens":
		s.geminiCount(w, r, model)
	default:
		writeGeminiError(w, 404, "dial has no method "+method+" on models; it has "+strings.Join(geminiMethods, ", "))
	}
}

// geminiGenerate serves generateContent and streamGenerateContent.
func (s *Server) geminiGenerate(w http.ResponseWriter, r *http.Request, model string, stream bool) {
	if _, _, ok := provider.Resolve(model); !ok {
		writeGeminiError(w, 404, gwUnknownModel(model))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeGeminiError(w, 400, err.Error())
		return
	}
	req, err := parseGemini(body, model, stream)
	if err != nil {
		writeGeminiError(w, 400, err.Error())
		return
	}
	s.gwServe(w, r, provider.Gemini, body, model, req)
}

// geminiCount answers countTokens. The count is a rough estimate made here;
// no upstream is asked. The model must still exist.
func (s *Server) geminiCount(w http.ResponseWriter, r *http.Request, model string) {
	if _, _, ok := provider.Resolve(model); !ok {
		writeGeminiError(w, 404, gwUnknownModel(model))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeGeminiError(w, 400, err.Error())
		return
	}
	n, err := geminiCountTokens(body)
	if err != nil {
		writeGeminiError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"totalTokens": n})
}

// geminiModelObject describes one catalog model the Gemini way.
func geminiModelObject(e provider.Entry) map[string]any {
	name := e.Name
	if name == "" {
		name = e.ID
	}
	return map[string]any{
		"name":                       "models/" + e.ID,
		"displayName":                name,
		"description":                name + " via dial (" + e.Provider.Name + ")",
		"supportedGenerationMethods": geminiMethods,
	}
}

// geminiModels lists the whole catalog.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		models = append(models, geminiModelObject(e))
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

// geminiModel describes one catalog model, by its catalog id.
func (s *Server) geminiModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, e := range provider.Catalog() {
		if e.ID == id {
			writeJSON(w, 200, geminiModelObject(e))
			return
		}
	}
	writeGeminiError(w, 404, gwUnknownModel(id))
}
