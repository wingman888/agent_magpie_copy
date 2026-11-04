package provider

import (
	"fmt"
	"strings"
)

// CheckFallback validates one fallback entry, "provider/model", against what
// magpie knows: the provider must exist and the model must be one it offers
// (in the vendor's list or catalog) or one the user has chosen to expose. It
// returns the entry in canonical form, with the provider's id.
func CheckFallback(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	pid, model, ok := strings.Cut(ref, "/")
	pid, model = strings.TrimSpace(pid), strings.TrimSpace(model)
	if !ok || pid == "" || model == "" {
		return "", fmt.Errorf("%q is not provider/model", ref)
	}
	p, err := Find(pid)
	if err != nil {
		return "", fmt.Errorf("fallback %q: no provider %q", ref, pid)
	}
	known := p.Chosen(model)
	if !known {
		for _, m := range p.Available() {
			if m.ID == model {
				known = true
				break
			}
		}
	}
	if !known {
		return "", fmt.Errorf("fallback %q: magpie does not know model %q on %s", ref, model, p.Name)
	}
	return p.ID + "/" + model, nil
}
