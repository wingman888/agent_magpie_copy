// Package claudebridge contains the small stdio MCP helper used by magpie's
// Claude Subscription bridge. The helper is launched by the genuine Claude
// Code binary; tool execution is handed back to the parent magpie process.
package claudebridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
)

type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type callbackRequest struct {
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name"`
	Arguments  json.RawMessage `json:"arguments"`
}

type callbackResponse struct {
	Content any  `json:"content"`
	IsError bool `json:"is_error,omitempty"`
}

// RunMCP runs the hidden stdio MCP subprocess. args are callback URL and tools
// JSON path. It intentionally implements only the MCP methods Claude Code needs.
func RunMCP(args []string) error {
	if len(args) != 2 {
		return errors.New("claude MCP helper expects callback URL and tools file")
	}
	callbackURL, toolsPath := args[0], args[1]
	b, err := os.ReadFile(toolsPath)
	if err != nil {
		return err
	}
	var tools []tool
	if err := json.Unmarshal(b, &tools); err != nil {
		return fmt.Errorf("read MCP tools: %w", err)
	}

	client := &http.Client{} // a tools/call intentionally blocks until the agent returns its result
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64<<10), 16<<20)
	enc := json.NewEncoder(os.Stdout)
	var outMu sync.Mutex
	respond := func(v any) {
		outMu.Lock()
		_ = enc.Encode(v)
		outMu.Unlock()
	}

	for in.Scan() {
		var req rpcRequest
		if json.Unmarshal(in.Bytes(), &req) != nil {
			continue
		}
		// Notifications have no id and must not receive a response.
		if len(req.ID) == 0 {
			continue
		}
		go func(req rpcRequest) {
			result := any(nil)
			var rpcErr any
			switch req.Method {
			case "initialize":
				result = map[string]any{
					"protocolVersion": "2025-06-18",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "magpie", "version": "1"},
				}
			case "tools/list":
				result = map[string]any{"tools": tools}
			case "tools/call":
				var p struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
					Meta      map[string]any  `json:"_meta"`
				}
				if err := json.Unmarshal(req.Params, &p); err != nil {
					rpcErr = map[string]any{"code": -32602, "message": err.Error()}
					break
				}
				id, _ := p.Meta["claudecode/toolUseId"].(string)
				if id == "" {
					rpcErr = map[string]any{"code": -32602, "message": "Claude Code omitted claudecode/toolUseId"}
					break
				}
				payload, _ := json.Marshal(callbackRequest{ToolCallID: id, Name: p.Name, Arguments: p.Arguments})
				httpReq, _ := http.NewRequest(http.MethodPost, callbackURL, bytes.NewReader(payload))
				httpReq.Header.Set("Content-Type", "application/json")
				res, err := client.Do(httpReq)
				if err != nil {
					rpcErr = map[string]any{"code": -32000, "message": err.Error()}
					break
				}
				body, _ := io.ReadAll(io.LimitReader(res.Body, 32<<20))
				res.Body.Close()
				if res.StatusCode != http.StatusOK {
					rpcErr = map[string]any{"code": -32000, "message": string(body)}
					break
				}
				var cb callbackResponse
				if err := json.Unmarshal(body, &cb); err != nil {
					rpcErr = map[string]any{"code": -32000, "message": err.Error()}
					break
				}
				result = map[string]any{"content": cb.Content, "isError": cb.IsError}
			default:
				rpcErr = map[string]any{"code": -32601, "message": "method not found"}
			}
			response := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID)}
			if rpcErr != nil {
				response["error"] = rpcErr
			} else {
				response["result"] = result
			}
			respond(response)
		}(req)
	}
	return in.Err()
}
