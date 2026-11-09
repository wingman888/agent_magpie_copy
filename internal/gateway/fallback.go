package gateway

import (
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// target is one way to serve a request: a candidate route through a provider,
// with the model that provider knows the request by. A signed-in Claude Code
// account is direct: it answers by itself, writes to the client, and so
// cannot be tried again after it fails.
type target struct {
	provider.Candidate
	model  string
	direct bool
}

// chain lists every way to serve a request, in the order to try them: the
// provider's own routes (its keys, most suitable first), then the routes of
// each provider on its fallback list, in the order the list gives. Ways that
// ran out recently go behind the others.
func (s *Server) chain(p provider.Provider, from provider.Protocol, model string, body []byte) []target {
	seen := map[string]bool{}
	var out []target
	add := func(q provider.Provider, m string) {
		k := q.ID + "\x00" + m
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, s.targets(q, from, m, body)...)
	}
	add(p, model)
	for _, entry := range p.Fallback {
		if q, m, ok := resolveFallback(entry); ok {
			add(q, m)
		}
	}
	return s.inOrder(out)
}

// targets lists the ways one provider can serve the request under a model.
func (s *Server) targets(p provider.Provider, from provider.Protocol, model string, body []byte) []target {
	if p.Account != nil && p.Account.Agent == "claude" {
		return []target{{Candidate: provider.Candidate{Provider: p}, model: model, direct: true}}
	}
	var out []target
	for _, c := range s.routes(p, from, model, body) {
		out = append(out, target{Candidate: c, model: model})
	}
	return out
}

// resolveFallback reads one fallback entry, "provider/model". An entry that
// does not name a usable provider and model is skipped without a word.
func resolveFallback(entry string) (provider.Provider, string, bool) {
	entry = strings.TrimSpace(entry)
	if !strings.Contains(entry, "/") {
		return provider.Provider{}, "", false
	}
	p, model, ok := provider.Resolve(entry)
	if !ok || model == "" {
		return provider.Provider{}, "", false
	}
	return p, model, true
}
