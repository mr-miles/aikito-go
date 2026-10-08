package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	mcpProtocolVersion  = "2025-11-25"
	mcpUserAgent        = "aikito"
	maxMCPResponseBytes = 8 * 1024 * 1024
)

// MCPProbeError mirrors _MCPProbeError.
type MCPProbeError struct{ Message string }

func (e *MCPProbeError) Error() string { return e.Message }

func probeErrorf(format string, args ...any) error {
	return &MCPProbeError{Message: fmt.Sprintf(format, args...)}
}

// RunLiveMCPCommands mirrors run_live_mcp_commands: runs one live MCP
// status command per agent, SKIPping if the executable isn't on PATH,
// classifying OK/ERROR/TIMEOUT. The animated terminal loading indicator is
// deliberately not ported (purely cosmetic, explicitly low priority).
func RunLiveMCPCommands(commands map[string][]string, timeout time.Duration) []LiveMCPResult {
	var results []LiveMCPResult
	for agent, command := range commands {
		if len(command) == 0 {
			results = append(results, LiveMCPResult{Agent: agent, Command: command, Status: "SKIP"})
			continue
		}
		if _, err := exec.LookPath(command[0]); err != nil {
			results = append(results, LiveMCPResult{Agent: agent, Command: command, Status: "SKIP"})
			continue
		}
		cmd := exec.Command(command[0], command[1:]...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		done := make(chan error, 1)
		if err := cmd.Start(); err != nil {
			results = append(results, LiveMCPResult{Agent: agent, Command: command, Status: "ERROR", Output: err.Error()})
			continue
		}
		go func() { done <- cmd.Wait() }()
		var runErr error
		select {
		case runErr = <-done:
		case <-time.After(timeout):
			_ = cmd.Process.Kill()
			<-done
			results = append(results, LiveMCPResult{Agent: agent, Command: command, Status: "TIMEOUT"})
			continue
		}
		outParts := []string{}
		if s := strings.TrimSpace(stdout.String()); s != "" {
			outParts = append(outParts, s)
		}
		if s := strings.TrimSpace(stderr.String()); s != "" {
			outParts = append(outParts, s)
		}
		status := "OK"
		code := 0
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			status = "ERROR"
			code = exitErr.ExitCode()
		} else if runErr != nil {
			status = "ERROR"
			code = -1
		}
		rc := code
		results = append(results, LiveMCPResult{Agent: agent, Command: command, Status: status, ReturnCode: &rc, Output: strings.Join(outParts, "\n")})
	}
	return results
}

// resolveMCPHeaders mirrors _resolve_mcp_headers: resolves env-ref-or-literal
// header values, erroring if an env-ref can't be resolved; materializes
// bearer_token_env_var into a synthesized Authorization header if present.
func resolveMCPHeaders(entry *OrderedObject) (map[string]string, error) {
	resolved := map[string]string{}

	if envHeadersVal, ok := entry.Get("env_http_headers"); ok {
		if envHeaders, ok := envHeadersVal.(*OrderedObject); ok {
			for _, name := range envHeaders.Keys() {
				envNameVal, _ := envHeaders.Get(name)
				envName, ok := envNameVal.(string)
				if !ok {
					continue
				}
				value, exists := os.LookupEnv(envName)
				if !exists {
					return nil, probeErrorf("credential environment variable '%s' is unavailable", envName)
				}
				resolved[name] = value
			}
		}
	}

	for _, containerName := range []string{"headers", "http_headers"} {
		headersVal, ok := entry.Get(containerName)
		if !ok {
			continue
		}
		headers, ok := headersVal.(*OrderedObject)
		if !ok {
			continue
		}
		for _, name := range headers.Keys() {
			rawVal, _ := headers.Get(name)
			rawValue, ok := rawVal.(string)
			if !ok {
				continue
			}
			if envName := EnvironmentReference(rawValue); envName != "" {
				value, exists := os.LookupEnv(envName)
				if !exists {
					return nil, probeErrorf("credential environment variable '%s' is unavailable", envName)
				}
				resolved[name] = value
			} else {
				resolved[name] = rawValue
			}
		}
	}

	if bearerVal, ok := entry.Get("bearer_token_env_var"); ok {
		if bearerEnv, ok := bearerVal.(string); ok && bearerEnv != "" {
			token, exists := os.LookupEnv(bearerEnv)
			if !exists {
				return nil, probeErrorf("credential environment variable '%s' is unavailable", bearerEnv)
			}
			resolved["Authorization"] = "Bearer " + token
		}
	}
	return resolved, nil
}

func headersContainCredentials(headers map[string]string) bool {
	for name := range headers {
		if IsCredentialHeader(name) {
			return true
		}
	}
	return false
}

// responseMessage mirrors _response_message: best-effort extraction of a
// human-readable message from an HTTP error body (JSON error/detail/message/
// title fields, or the raw text if not JSON).
func responseMessage(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return text
	}
	if errVal, ok := payload["error"].(map[string]any); ok {
		if msg, ok := errVal["message"].(string); ok {
			return msg
		}
	}
	for _, key := range []string{"detail", "message", "title"} {
		if msg, ok := payload[key].(string); ok {
			return msg
		}
	}
	return ""
}

var sseEventSplit = regexp.MustCompile(`\r?\n\r?\n`)

// decodeMCPResponse mirrors _decode_mcp_response: tolerates both a bare JSON
// body and an SSE-framed body, scanning candidates for one whose "id"
// matches request_id.
func decodeMCPResponse(body []byte, requestID int) (map[string]any, error) {
	text := strings.TrimSpace(string(body))
	var candidates []string
	if strings.HasPrefix(text, "data:") || strings.Contains(text, "\ndata:") {
		for _, event := range sseEventSplit.Split(text, -1) {
			var dataLines []string
			for _, line := range strings.Split(event, "\n") {
				if strings.HasPrefix(line, "data:") {
					dataLines = append(dataLines, strings.TrimLeft(strings.TrimPrefix(line, "data:"), " \t"))
				}
			}
			if data := strings.Join(dataLines, "\n"); data != "" {
				candidates = append(candidates, data)
			}
		}
	} else if text != "" {
		candidates = append(candidates, text)
	}

	for _, candidate := range candidates {
		var payload map[string]any
		if err := json.Unmarshal([]byte(candidate), &payload); err != nil {
			continue
		}
		idVal, ok := payload["id"]
		if !ok {
			continue
		}
		idNum, ok := idVal.(float64)
		if !ok || int(idNum) != requestID {
			continue
		}
		if errVal, ok := payload["error"].(map[string]any); ok {
			msg := "MCP request failed"
			if m, ok := errVal["message"].(string); ok {
				msg = m
			}
			return nil, probeErrorf("%s", msg)
		}
		if result, ok := payload["result"].(map[string]any); ok {
			return result, nil
		}
		return nil, probeErrorf("MCP response has no result object")
	}
	return nil, probeErrorf("MCP response did not contain the requested JSON-RPC result")
}

// rejectRedirectsClient mirrors _RejectRedirects: refuse to follow any HTTP
// redirect, so a hijacked/misconfigured server can't redirect credentialed
// requests off to an attacker-controlled host.
func rejectRedirectsClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// postMCPMessage mirrors _post_mcp_message: POST a JSON-RPC message with
// retry-with-backoff on 429/5xx or transport errors, an 8 MiB response cap,
// and redirect rejection.
func postMCPMessage(targetURL string, payload map[string]any, headers map[string]string, timeout time.Duration, sessionID, protocolVersion string, retries int) ([]byte, string, error) {
	method, _ := payload["method"].(string)
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	client := rejectRedirectsClient(timeout)

	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		req, err := http.NewRequest(http.MethodPost, targetURL, bytes.NewReader(data))
		if err != nil {
			return nil, "", err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if req.Header.Get("User-Agent") == "" {
			req.Header.Set("User-Agent", mcpUserAgent)
		}
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Mcp-Method", method)
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
		if protocolVersion != "" {
			req.Header.Set("MCP-Protocol-Version", protocolVersion)
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			if attempt < retries {
				time.Sleep(time.Duration(300*(1<<attempt)) * time.Millisecond)
				continue
			}
			return nil, "", probeErrorf("connection failed: %v", err)
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxMCPResponseBytes+1))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if attempt < retries {
				time.Sleep(time.Duration(300*(1<<attempt)) * time.Millisecond)
				continue
			}
			return nil, "", probeErrorf("connection failed: %v", readErr)
		}
		if len(body) > maxMCPResponseBytes {
			return nil, "", probeErrorf("MCP response exceeded the 8 MiB safety limit")
		}

		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return nil, "", probeErrorf("HTTP %d: redirect refused", resp.StatusCode)
		}
		if resp.StatusCode >= 400 {
			detail := responseMessage(body)
			if (resp.StatusCode == 429 || resp.StatusCode == 500 || resp.StatusCode == 502 ||
				resp.StatusCode == 503 || resp.StatusCode == 504) && attempt < retries {
				time.Sleep(time.Duration(300*(1<<attempt)) * time.Millisecond)
				continue
			}
			suffix := ""
			if detail != "" {
				suffix = ": " + detail
			}
			return nil, "", probeErrorf("HTTP %d%s", resp.StatusCode, suffix)
		}
		return body, resp.Header.Get("Mcp-Session-Id"), nil
	}
	if lastErr != nil {
		return nil, "", probeErrorf("MCP request failed after retries: %v", lastErr)
	}
	return nil, "", probeErrorf("MCP request failed after retries")
}

// listRemoteMCPTools mirrors _list_remote_mcp_tools: initialize ->
// notifications/initialized -> paginated tools/list (hard cap 100 pages).
func listRemoteMCPTools(targetURL string, headers map[string]string, timeout time.Duration) ([]string, error) {
	initialize := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "aikito", "version": "1"},
		},
	}
	body, sessionID, err := postMCPMessage(targetURL, initialize, headers, timeout, "", "", 2)
	if err != nil {
		return nil, err
	}
	initialized, err := decodeMCPResponse(body, 1)
	if err != nil {
		return nil, err
	}
	protocolVersion, _ := initialized["protocolVersion"].(string)
	if protocolVersion == "" {
		return nil, probeErrorf("MCP initialize response has no protocol version")
	}

	_, _, err = postMCPMessage(targetURL, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}, headers, timeout, sessionID, protocolVersion, 2)
	if err != nil {
		return nil, err
	}

	var names []string
	cursor := ""
	for requestID := 2; requestID < 102; requestID++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		body, _, err := postMCPMessage(targetURL, map[string]any{
			"jsonrpc": "2.0", "id": requestID, "method": "tools/list", "params": params,
		}, headers, timeout, sessionID, protocolVersion, 2)
		if err != nil {
			return nil, err
		}
		result, err := decodeMCPResponse(body, requestID)
		if err != nil {
			return nil, err
		}
		toolsVal, ok := result["tools"].([]any)
		if !ok {
			return nil, probeErrorf("MCP tools/list response has no tools array")
		}
		for _, t := range toolsVal {
			if tm, ok := t.(map[string]any); ok {
				if name, ok := tm["name"].(string); ok {
					names = append(names, name)
				}
			}
		}
		nextCursor, _ := result["nextCursor"].(string)
		if nextCursor == "" {
			return names, nil
		}
		cursor = nextCursor
	}
	return nil, probeErrorf("MCP tools/list pagination exceeded 100 pages")
}

// ProbeMCPTools mirrors probe_mcp_tools: a read-only health/introspection
// check that speaks raw MCP-over-HTTP JSON-RPC directly (never shells out
// to the agent CLI).
func ProbeMCPTools(spec AgentSpec, timeout time.Duration) MCPToolProbeResult {
	if info, err := os.Stat(spec.ConfigPath); err != nil || !info.Mode().IsRegular() {
		return MCPToolProbeResult{Agent: spec.Agent, Status: "ERROR", AuthMethod: "Unknown", Error: "config missing"}
	}
	authMethod := "Unknown"
	var headers map[string]string

	text, err := readFileString(spec.ConfigPath)
	if err != nil {
		return MCPToolProbeResult{Agent: spec.Agent, Status: "ERROR", AuthMethod: authMethod, Error: RedactProbeError(err.Error(), headers)}
	}
	entry, err := ReadEntry(spec, text)
	if err != nil {
		return MCPToolProbeResult{Agent: spec.Agent, Status: "ERROR", AuthMethod: authMethod, Error: RedactProbeError(err.Error(), headers)}
	}
	if entry == nil {
		return MCPToolProbeResult{Agent: spec.Agent, Status: "ERROR", AuthMethod: authMethod, Error: "managed entry missing"}
	}
	authMethod = DescribeMCPAuth(entry)
	if authMethod == "OAuth" {
		return MCPToolProbeResult{Agent: spec.Agent, Status: "SKIP", AuthMethod: authMethod, Error: "OAuth credentials are managed by the Agent runtime"}
	}

	var targetURL string
	if v, ok := entry.Get("url"); ok {
		targetURL, _ = v.(string)
	}
	if targetURL == "" {
		if v, ok := entry.Get("serverUrl"); ok {
			targetURL, _ = v.(string)
		}
	}
	if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") {
		return MCPToolProbeResult{Agent: spec.Agent, Status: "SKIP", AuthMethod: authMethod, Error: "only remote HTTP MCP servers are supported"}
	}

	headers, err = resolveMCPHeaders(entry)
	if err != nil {
		return MCPToolProbeResult{Agent: spec.Agent, Status: "ERROR", AuthMethod: authMethod, Error: RedactProbeError(err.Error(), headers)}
	}

	parsedURL, err := url.Parse(targetURL)
	if err != nil {
		return MCPToolProbeResult{Agent: spec.Agent, Status: "ERROR", AuthMethod: authMethod, Error: RedactProbeError(err.Error(), headers)}
	}
	hasURLCredentials := parsedURL.User != nil
	if parsedURL.Scheme == "http" && !IsLoopbackURL(targetURL) && (headersContainCredentials(headers) || hasURLCredentials) {
		return MCPToolProbeResult{Agent: spec.Agent, Status: "SKIP", AuthMethod: authMethod, Error: "refusing to send MCP credentials over non-loopback HTTP"}
	}

	toolNames, err := listRemoteMCPTools(targetURL, headers, timeout)
	if err != nil {
		return MCPToolProbeResult{Agent: spec.Agent, Status: "ERROR", AuthMethod: authMethod, Error: RedactProbeError(err.Error(), headers)}
	}
	return MCPToolProbeResult{Agent: spec.Agent, Status: "OK", AuthMethod: authMethod, ToolNames: toolNames}
}

// ProbeMCPToolsForSpecs mirrors probe_mcp_tools_for_specs: runs independent
// read-only probes concurrently (bounded worker pool, min(8, len(specs))),
// preserving input order in the output slice (an indexed results slice +
// WaitGroup, not a channel that could reorder).
func ProbeMCPToolsForSpecs(specs []AgentSpec, timeout time.Duration) []MCPToolProbeResult {
	if len(specs) == 0 {
		return nil
	}
	results := make([]MCPToolProbeResult, len(specs))
	workers := len(specs)
	if workers > 8 {
		workers = 8
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := range specs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = ProbeMCPTools(specs[i], timeout)
		}(i)
	}
	wg.Wait()
	return results
}
