package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Self-contained (no live-network cross-validation practical here): a fake
// MCP-over-HTTP server exercising initialize -> notifications/initialized ->
// paginated tools/list, confirming the JSON-RPC client logic end-to-end.
func TestListRemoteMCPToolsHappyPath(t *testing.T) {
	page := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		method, _ := req["method"].(string)
		id := req["id"]

		switch method {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{"protocolVersion": mcpProtocolVersion},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusOK)
		case "tools/list":
			page++
			var result map[string]any
			if page == 1 {
				result = map[string]any{
					"tools":      []any{map[string]any{"name": "tool-a"}},
					"nextCursor": "page2",
				}
			} else {
				result = map[string]any{
					"tools": []any{map[string]any{"name": "tool-b"}},
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		default:
			t.Errorf("unexpected method %q", method)
		}
	}))
	defer server.Close()

	names, err := listRemoteMCPTools(server.URL, map[string]string{}, 5*time.Second)
	if err != nil {
		t.Fatalf("listRemoteMCPTools: %v", err)
	}
	if len(names) != 2 || names[0] != "tool-a" || names[1] != "tool-b" {
		t.Errorf("names = %v, want [tool-a tool-b]", names)
	}
}

func TestListRemoteMCPToolsRejectsRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://attacker.example.com/steal", http.StatusFound)
	}))
	defer target.Close()

	_, err := listRemoteMCPTools(target.URL, map[string]string{}, 5*time.Second)
	if err == nil {
		t.Fatal("expected an error for a redirecting server, got nil")
	}
}

func TestListRemoteMCPToolsErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req["id"],
			"error": map[string]any{"message": "boom"},
		})
	}))
	defer server.Close()

	_, err := listRemoteMCPTools(server.URL, map[string]string{}, 5*time.Second)
	if err == nil || err.Error() != "boom" {
		t.Errorf("err = %v, want \"boom\"", err)
	}
}

func TestDescribeMCPAuthVariants(t *testing.T) {
	cases := []struct {
		name  string
		entry *OrderedObject
		want  string
	}{
		{"none", NewOrderedObject(), "None"},
		{"oauth_true", OO("oauth", true), "OAuth"},
		{"oauth_string", OO("auth", "oauth"), "OAuth"},
		{"bearer_env_var", OO("bearer_token_env_var", "MY_TOKEN"), "Bearer · env token"},
		{"inline_bearer_header", OO("headers", OO("Authorization", "Bearer xyz")), "Bearer · inline header"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DescribeMCPAuth(tc.entry)
			if got != tc.want {
				t.Errorf("DescribeMCPAuth() = %q, want %q", got, tc.want)
			}
		})
	}
}
