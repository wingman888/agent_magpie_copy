package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// copilotDefaultAPI is the Copilot API used when a session does not name one.
const copilotDefaultAPI = "https://api.githubcopilot.com"

// CopilotTokenURL exchanges a GitHub token for a short-lived Copilot session.
// A variable so tests can point it at a local fake server.
var CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

// errCopilotSignedOut means GitHub no longer accepts the stored login.
var errCopilotSignedOut = errors.New("Copilot 会话失效，请重新登录 Copilot")

// copilotSession is the answer to a token exchange.
type copilotSession struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	Endpoints struct {
		API string `json:"api"`
	} `json:"endpoints"`
}

// copilotSessionFor trades the GitHub token in apps.json for a session. The
// session names the API address to use.
func copilotSessionFor(ctx context.Context, oauth string) (copilotSession, error) {
	var s copilotSession
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return s, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "token "+oauth)
	req.Header.Set("User-Agent", "dial")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return s, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	switch {
	case res.StatusCode == http.StatusUnauthorized, res.StatusCode == http.StatusForbidden, res.StatusCode == http.StatusNotFound:
		return s, errCopilotSignedOut
	case res.StatusCode != http.StatusOK:
		return s, fmt.Errorf("copilot token: %s", res.Status)
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("copilot token: not a session")
	}
	if s.Token == "" {
		return s, errCopilotSignedOut
	}
	if s.Endpoints.API == "" {
		s.Endpoints.API = copilotDefaultAPI
	}
	return s, nil
}

// copilotRow is one entry of the Copilot model list.
type copilotRow struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	ModelPickerEnabled *bool  `json:"model_picker_enabled"`
	Capabilities       struct {
		Type string `json:"type"`
	} `json:"capabilities"`
	Policy *struct {
		State string `json:"state"`
	} `json:"policy"`
}

// copilotChatModels keeps the models this account can chat with: chat type,
// enabled by policy, shown in the picker (not internal), not embeddings and
// not "auto". The server's order is kept.
func copilotChatModels(rows []copilotRow) []catalog.Model {
	var out []catalog.Model
	seen := map[string]bool{}
	for _, r := range rows {
		id := strings.TrimSpace(r.ID)
		switch {
		case id == "" || id == "auto" || seen[id]:
		case r.Capabilities.Type != "chat":
		case strings.Contains(id, "embed") || strings.Contains(id, "internal"):
		case r.ModelPickerEnabled != nil && !*r.ModelPickerEnabled:
		case r.Policy != nil && r.Policy.State != "enabled":
		default:
			name := r.Name
			if name == "" {
				name = id
			}
			seen[id] = true
			out = append(out, catalog.Model{ID: id, Name: name})
		}
	}
	return out
}

// fetchCopilotModels asks the signed-in account which chat models it may use.
// It returns the API base that answered, and the filtered list.
func fetchCopilotModels(ctx context.Context) (string, []catalog.Model, error) {
	_, oauth, ok := copilotLogin()
	if !ok {
		return "", nil, errCopilotSignedOut
	}
	sess, err := copilotSessionFor(ctx, oauth)
	if err != nil {
		return "", nil, err
	}
	base := strings.TrimRight(sess.Endpoints.API, "/")
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	req.Header.Set("User-Agent", "dial")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return "", nil, errCopilotSignedOut
	}
	if res.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("copilot models: %s", res.Status)
	}
	var v struct {
		Data []copilotRow `json:"data"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return "", nil, fmt.Errorf("copilot models: not a model list")
	}
	ms := copilotChatModels(v.Data)
	if len(ms) == 0 {
		return "", nil, errors.New("this Copilot account lists no chat models")
	}
	return base, ms, nil
}
