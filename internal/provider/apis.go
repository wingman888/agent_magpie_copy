package provider

import (
	"slices"

	"github.com/yetone/magpie/internal/catalog"
)

// APIs says which of the provider's APIs its latest model list declares
// serve model, in the order the list gives them. Nil when the list is
// missing, does not name the model, or says nothing about its APIs: the
// caller then treats every API the provider speaks as possible.
func (p Provider) APIs(model string) []Protocol {
	live, _, ok := catalog.Live(p.ID)
	if !ok {
		return nil
	}
	for _, m := range live {
		if m.ID != model {
			continue
		}
		var out []Protocol
		for _, a := range m.APIs {
			var pr Protocol
			switch a {
			case "chat":
				pr = Chat
			case "responses":
				pr = Responses
			case "anthropic":
				pr = Anthropic
			default:
				continue
			}
			if !slices.Contains(out, pr) {
				out = append(out, pr)
			}
		}
		return out
	}
	return nil
}

// copilotAPIs maps the endpoints Copilot lists for a model onto magpie's API
// names. Endpoints magpie does not speak (websocket, embeddings) are skipped;
// a list that names none of the three is unknown, so nil.
func copilotAPIs(endpoints []string) []string {
	var out []string
	for _, e := range endpoints {
		var api string
		switch e {
		case "/chat/completions":
			api = "chat"
		case "/responses":
			api = "responses"
		case "/v1/messages":
			api = "anthropic"
		default:
			continue
		}
		if !slices.Contains(out, api) {
			out = append(out, api)
		}
	}
	return out
}
