package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- fixtures ---

// fakeMCPServer is a minimal MCP-over-HTTP server answering initialize /
// notifications/initialized / tools/list with a single named tool. It
// records the last Authorization header it saw and optionally sleeps
// before answering tools/list (to force out-of-order completion).
type fakeMCPServer struct {
	*httptest.Server
	lastAuth atomic.Value // string
}

func newFakeMCPServer(t *testing.T, toolName string, delay time.Duration) *fakeMCPServer {
	t.Helper()
	f := &fakeMCPServer{}
	f.lastAuth.Store("")
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastAuth.Store(r.Header.Get("Authorization"))
		var req map[string]any
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		method, _ := req["method"].(string)
		id := req["id"]
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "initialize":
			json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{"protocolVersion": mcpProtocolVersion},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusOK)
		case "tools/list":
			if delay > 0 {
				time.Sleep(delay)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{"tools": []any{map[string]any{"name": toolName}}},
			})
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// claudeSpec writes a .claude.json-shaped config containing entry under
// mcpServers.weather (or no file at all when entry == nil and
// writeFile == false) and returns a matching AgentSpec.
func claudeSpec(t *testing.T, entry map[string]any, writeFile bool) AgentSpec {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	if writeFile {
		servers := map[string]any{}
		if entry != nil {
			servers["weather"] = entry
		}
		data, err := json.Marshal(map[string]any{"mcpServers": servers})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return AgentSpec{
		Agent: "claude-code", Server: "weather", ConfigPath: path,
		ConfigFormat: "claude_json", Adapter: "claude_json", TargetName: "weather",
	}
}

// --- ProbeMCPTools ---

func TestProbeMCPToolsShortCircuits(t *testing.T) {
	t.Setenv("AIKITO_TEST_MISSING_VAR", "")
	os.Unsetenv("AIKITO_TEST_MISSING_VAR")

	cases := []struct {
		name       string
		spec       AgentSpec
		wantStatus string
		wantError  string
	}{
		{"config_missing", claudeSpec(t, nil, false), "ERROR", "config missing"},
		{"entry_missing", claudeSpec(t, nil, true), "ERROR", "managed entry missing"},
		{"oauth_skipped", claudeSpec(t, map[string]any{"type": "http", "url": "https://example.com/mcp", "oauth": true}, true),
			"SKIP", "OAuth credentials are managed by the Agent runtime"},
		{"stdio_skipped", claudeSpec(t, map[string]any{"command": "npx", "args": []any{"srv"}}, true),
			"SKIP", "only remote HTTP MCP servers are supported"},
		{"unresolvable_env_header", claudeSpec(t, map[string]any{
			"type": "http", "url": "https://example.com/mcp",
			"headers": map[string]any{"X-Api-Key": "${AIKITO_TEST_MISSING_VAR}"},
		}, true), "ERROR", "credential environment variable 'AIKITO_TEST_MISSING_VAR' is unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ProbeMCPTools(tc.spec, time.Second)
			if got.Status != tc.wantStatus || got.Error != tc.wantError {
				t.Errorf("ProbeMCPTools() = {Status:%q Error:%q}, want {Status:%q Error:%q}",
					got.Status, got.Error, tc.wantStatus, tc.wantError)
			}
			if got.Agent != "claude-code" {
				t.Errorf("Agent = %q, want claude-code", got.Agent)
			}
		})
	}
}

func TestProbeMCPToolsInvalidConfigIsError(t *testing.T) {
	spec := claudeSpec(t, nil, false)
	if err := os.WriteFile(spec.ConfigPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ProbeMCPTools(spec, time.Second)
	if got.Status != "ERROR" || got.Error == "" {
		t.Errorf("got %+v, want an ERROR with a message", got)
	}
}

// The single most safety-critical branch: credentials must never be sent
// over plaintext HTTP to a non-loopback host. Each case here must be
// refused BEFORE any network I/O (all target hosts are unroutable/fake, so
// a missing gate would surface as a network ERROR, not this SKIP).
func TestProbeMCPToolsRefusesCredentialsOverNonLoopbackHTTP(t *testing.T) {
	t.Setenv("AIKITO_TEST_TOKEN", "s3cret-token")
	const refusal = "refusing to send MCP credentials over non-loopback HTTP"
	cases := []struct {
		name  string
		entry map[string]any
	}{
		{"inline_authorization_header", map[string]any{
			"type": "http", "url": "http://mcp.example.com/mcp",
			"headers": map[string]any{"Authorization": "Bearer abc"}}},
		{"env_ref_api_key_header", map[string]any{
			"type": "http", "url": "http://mcp.example.com/mcp",
			"headers": map[string]any{"X-Api-Key": "${AIKITO_TEST_TOKEN}"}}},
		{"bearer_token_env_var", map[string]any{
			"type": "http", "url": "http://mcp.example.com/mcp",
			"bearer_token_env_var": "AIKITO_TEST_TOKEN"}},
		{"userinfo_in_url", map[string]any{
			"type": "http", "url": "http://user:pass@mcp.example.com/mcp"}},
		// "localhost" as a mere substring of a public hostname is NOT loopback.
		{"localhost_substring_host", map[string]any{
			"type": "http", "url": "http://localhost.evil.example/mcp",
			"headers": map[string]any{"Authorization": "Bearer abc"}}},
		{"serverUrl_key_variant", map[string]any{
			"serverUrl": "http://mcp.example.com/mcp",
			"headers":   map[string]any{"Cookie": "session=abc"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ProbeMCPTools(claudeSpec(t, tc.entry, true), 200*time.Millisecond)
			if got.Status != "SKIP" || got.Error != refusal {
				t.Errorf("got {Status:%q Error:%q}, want SKIP %q", got.Status, got.Error, refusal)
			}
		})
	}
}

// The inverse: loopback plain-HTTP WITH credentials must be allowed, and
// the credential must actually reach the server.
func TestProbeMCPToolsAllowsCredentialsOverLoopbackHTTP(t *testing.T) {
	srv := newFakeMCPServer(t, "forecast", 0)
	if !strings.HasPrefix(srv.URL, "http://127.0.0.1") {
		t.Fatalf("expected a plain-http loopback httptest URL, got %s", srv.URL)
	}
	spec := claudeSpec(t, map[string]any{
		"type": "http", "url": srv.URL,
		"headers": map[string]any{"Authorization": "Bearer loopback-ok"},
	}, true)
	got := ProbeMCPTools(spec, 2*time.Second)
	if got.Status != "OK" {
		t.Fatalf("got %+v, want OK", got)
	}
	if len(got.ToolNames) != 1 || got.ToolNames[0] != "forecast" {
		t.Errorf("ToolNames = %v, want [forecast]", got.ToolNames)
	}
	if got.AuthMethod != "Bearer · inline header" {
		t.Errorf("AuthMethod = %q", got.AuthMethod)
	}
	if auth, _ := srv.lastAuth.Load().(string); auth != "Bearer loopback-ok" {
		t.Errorf("server saw Authorization %q, want the configured credential", auth)
	}
}

func TestProbeMCPToolsServerUrlFallback(t *testing.T) {
	srv := newFakeMCPServer(t, "t1", 0)
	got := ProbeMCPTools(claudeSpec(t, map[string]any{"serverUrl": srv.URL}, true), 2*time.Second)
	if got.Status != "OK" || len(got.ToolNames) != 1 {
		t.Errorf("got %+v, want OK via serverUrl", got)
	}
}

// A server-side failure message that echoes the credential back must be
// redacted before it reaches the result.
func TestProbeMCPToolsRedactsSecretInError(t *testing.T) {
	const secret = "super-secret-value-123"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req["id"],
			"error": map[string]any{"message": "bad token: " + r.Header.Get("X-Api-Key")},
		})
	}))
	defer srv.Close()
	spec := claudeSpec(t, map[string]any{
		"type": "http", "url": srv.URL,
		"headers": map[string]any{"X-Api-Key": secret},
	}, true)
	got := ProbeMCPTools(spec, 2*time.Second)
	if got.Status != "ERROR" {
		t.Fatalf("got %+v, want ERROR", got)
	}
	if strings.Contains(got.Error, secret) {
		t.Errorf("secret leaked into error: %q", got.Error)
	}
	if !strings.Contains(got.Error, "<redacted>") {
		t.Errorf("expected <redacted> marker in %q", got.Error)
	}
}

// --- ProbeMCPToolsForSpecs ---

func TestProbeMCPToolsForSpecsEmpty(t *testing.T) {
	if got := ProbeMCPToolsForSpecs(nil, time.Second); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

// Staggered delays force completion order (fast, medium, slow) to differ
// from input order (slow, error, fast, medium); results must still line up
// with inputs, and one spec's failure must not bleed into the others.
func TestProbeMCPToolsForSpecsPreservesInputOrder(t *testing.T) {
	slow := newFakeMCPServer(t, "slow-tool", 300*time.Millisecond)
	fast := newFakeMCPServer(t, "fast-tool", 0)
	medium := newFakeMCPServer(t, "medium-tool", 150*time.Millisecond)

	specs := []AgentSpec{
		claudeSpec(t, map[string]any{"type": "http", "url": slow.URL}, true),
		claudeSpec(t, nil, false), // config missing -> ERROR
		claudeSpec(t, map[string]any{"type": "http", "url": fast.URL}, true),
		claudeSpec(t, map[string]any{"type": "http", "url": medium.URL}, true),
	}
	specs[1].Agent = "broken-agent"

	start := time.Now()
	got := ProbeMCPToolsForSpecs(specs, 2*time.Second)
	elapsed := time.Since(start)

	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	want := []struct{ status, tool string }{
		{"OK", "slow-tool"}, {"ERROR", ""}, {"OK", "fast-tool"}, {"OK", "medium-tool"},
	}
	for i, w := range want {
		if got[i].Status != w.status {
			t.Errorf("result[%d].Status = %q, want %q (%+v)", i, got[i].Status, w.status, got[i])
		}
		if w.tool != "" && (len(got[i].ToolNames) != 1 || got[i].ToolNames[0] != w.tool) {
			t.Errorf("result[%d].ToolNames = %v, want [%s]", i, got[i].ToolNames, w.tool)
		}
	}
	if got[1].Agent != "broken-agent" || got[1].Error != "config missing" {
		t.Errorf("result[1] = %+v, want broken-agent/config missing", got[1])
	}
	// Concurrency sanity check: serial execution would take >= 450ms.
	if elapsed > 440*time.Millisecond {
		t.Errorf("probes appear to run serially (took %v)", elapsed)
	}
}

// --- RunLiveMCPCommands ---

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunLiveMCPCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell scripts as fake agent CLIs")
	}
	dir := t.TempDir()
	okBin := writeScript(t, dir, "ok-agent", `echo "server-a connected"; echo "warning on stderr" >&2; exit 0`)
	failBin := writeScript(t, dir, "fail-agent", `echo "boom" >&2; exit 3`)
	// exec replaces the shell so Kill() hits the sleeping process directly
	// rather than orphaning a child that keeps the output pipe open.
	slowBin := writeScript(t, dir, "slow-agent", `exec sleep 5`)

	commands := map[string][]string{
		"ok":      {okBin, "mcp", "list"},
		"fail":    {failBin},
		"slow":    {slowBin},
		"missing": {"aikito-definitely-not-installed-xyz", "mcp", "list"},
		"empty":   {},
	}
	start := time.Now()
	results := RunLiveMCPCommands(commands, 300*time.Millisecond)
	if time.Since(start) > 3*time.Second {
		t.Errorf("timeout path did not kill the slow command promptly")
	}
	if len(results) != len(commands) {
		t.Fatalf("len = %d, want %d", len(results), len(commands))
	}
	byAgent := map[string]LiveMCPResult{}
	for _, r := range results {
		byAgent[r.Agent] = r
	}

	if r := byAgent["ok"]; r.Status != "OK" || r.ReturnCode == nil || *r.ReturnCode != 0 ||
		r.Output != "server-a connected\nwarning on stderr" {
		t.Errorf("ok = %+v (output %q)", r, r.Output)
	}
	if r := byAgent["fail"]; r.Status != "ERROR" || r.ReturnCode == nil || *r.ReturnCode != 3 || r.Output != "boom" {
		t.Errorf("fail = %+v", r)
	}
	if r := byAgent["slow"]; r.Status != "TIMEOUT" || r.ReturnCode != nil {
		t.Errorf("slow = %+v", r)
	}
	if r := byAgent["missing"]; r.Status != "SKIP" {
		t.Errorf("missing = %+v", r)
	}
	if r := byAgent["empty"]; r.Status != "SKIP" {
		t.Errorf("empty = %+v", r)
	}
}

// --- small helpers ---

func TestBoolEnvStr(t *testing.T) {
	if boolEnvStr(true) != "1" || boolEnvStr(false) != "0" {
		t.Errorf("boolEnvStr = %q/%q, want 1/0", boolEnvStr(true), boolEnvStr(false))
	}
}

func TestHeadersContainCredentials(t *testing.T) {
	cases := []struct {
		headers map[string]string
		want    bool
	}{
		{nil, false},
		{map[string]string{"Accept": "json", "X-Trace": "1"}, false},
		{map[string]string{"Authorization": "x"}, true},
		{map[string]string{"x-api-key": "x"}, true},
		{map[string]string{"Cookie": "x"}, true},
	}
	for _, tc := range cases {
		if got := headersContainCredentials(tc.headers); got != tc.want {
			t.Errorf("headersContainCredentials(%v) = %v, want %v", tc.headers, got, tc.want)
		}
	}
}

func TestResolveMCPHeaders(t *testing.T) {
	t.Setenv("AIKITO_T_A", "alpha")
	t.Setenv("AIKITO_T_B", "beta")
	t.Setenv("AIKITO_T_TOKEN", "tok")
	t.Setenv("AIKITO_T_GONE", "")
	os.Unsetenv("AIKITO_T_GONE")

	entry := OO(
		"env_http_headers", OO("X-Env", "AIKITO_T_A", "X-NonString", int64(5)),
		"headers", OO("X-Dollar", "${AIKITO_T_B}", "X-Literal", "plain", "X-Num", int64(1)),
		"http_headers", OO("X-Opencode", "{env:AIKITO_T_A}"),
		"bearer_token_env_var", "AIKITO_T_TOKEN",
	)
	got, err := resolveMCPHeaders(entry)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"X-Env": "alpha", "X-Dollar": "beta", "X-Literal": "plain",
		"X-Opencode": "alpha", "Authorization": "Bearer tok",
	}
	if len(got) != len(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("got[%q] = %q, want %q", k, got[k], v)
		}
	}

	errCases := map[string]*OrderedObject{
		"env_http_headers": OO("env_http_headers", OO("Authorization", "AIKITO_T_GONE")),
		"headers_ref":      OO("headers", OO("Authorization", "${AIKITO_T_GONE}")),
		"bearer":           OO("bearer_token_env_var", "AIKITO_T_GONE"),
	}
	for name, e := range errCases {
		_, err := resolveMCPHeaders(e)
		if err == nil || err.Error() != "credential environment variable 'AIKITO_T_GONE' is unavailable" {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	if got, err := resolveMCPHeaders(NewOrderedObject()); err != nil || len(got) != 0 {
		t.Errorf("empty entry: got %v, %v", got, err)
	}
}

func TestResponseMessage(t *testing.T) {
	cases := []struct{ body, want string }{
		{"", ""},
		{"   \n", ""},
		{"plain text failure", "plain text failure"},
		{`{"error": {"message": "nested msg"}, "detail": "d"}`, "nested msg"},
		{`{"detail": "the detail", "message": "m"}`, "the detail"},
		{`{"message": "the message", "title": "t"}`, "the message"},
		{`{"title": "the title"}`, "the title"},
		{`{"error": "string not object", "unrelated": 1}`, ""},
		{`[1, 2]`, "[1, 2]"},
	}
	for _, tc := range cases {
		if got := responseMessage([]byte(tc.body)); got != tc.want {
			t.Errorf("responseMessage(%q) = %q, want %q", tc.body, got, tc.want)
		}
	}
}

func TestWriteBrowserHelperRecordsOnlyURLs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell helper")
	}
	dir := t.TempDir()
	helper, err := writeBrowserHelper(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(helper)
	if err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("helper not executable: %v %v", info, err)
	}
	urlFile := filepath.Join(dir, "urls")
	cmd := exec.Command(helper, "--new-window", "https://a.example/authorize", "not-a-url", "http://b.example/x")
	cmd.Env = append(os.Environ(), "AIKITO_AUTH_URL_FILE="+urlFile, "AIKITO_OPEN_BROWSER=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper failed: %v %s", err, out)
	}
	data, _ := os.ReadFile(urlFile)
	if string(data) != "https://a.example/authorize\nhttp://b.example/x\n" {
		t.Errorf("url file = %q", data)
	}
}

// --- AuthenticateMCP ---

type authFixture struct {
	home, aikitoDir, configPath string
}

// newAuthFixture builds a workspace with a codex agent whose auth_command
// is authCommand (TOML array literal, or "" for none) and one MCP server
// "my-server". If synced, the codex config already holds exactly the
// desired entry.
func newAuthFixture(t *testing.T, authCommand, mcpExtra string, synced bool) authFixture {
	t.Helper()
	home := t.TempDir()
	aikitoDir := filepath.Join(home, "aikito")
	for _, d := range []string{"agents", "subagents", "mcps"} {
		if err := os.MkdirAll(filepath.Join(aikitoDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	must := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(aikitoDir, "layout.toml"), "version = 2\n")
	agent := `[agents.codex]
display_name = "Codex"
instruction_path = ".codex/AGENTS.md"

[agents.codex.detect]
commands = ["codex-nonexistent-binary-xyz"]
paths = [".codex"]

[agents.codex.mcp]
config_path = ".codex/config.toml"
config_format = "toml"
name_style = "underscore"
`
	if authCommand != "" {
		agent += "auth_command = " + authCommand + "\n"
	}
	must(filepath.Join(aikitoDir, "agents", "codex.toml"), agent)
	must(filepath.Join(aikitoDir, "mcps", "my-server.toml"),
		"transport = \"remote\"\nurl = \"https://example.com/mcp\"\nagents = [\"codex\"]\n"+mcpExtra)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, ".codex", "config.toml")
	if synced {
		must(configPath, "[mcp_servers.my_server]\nurl = \"https://example.com/mcp\"\n")
	}
	return authFixture{home: home, aikitoDir: aikitoDir, configPath: configPath}
}

func runAuth(f authFixture, agent, server string) (bool, error, string) {
	var out strings.Builder
	ok, err := AuthenticateMCP(f.aikitoDir, f.home, agent, server, func(s string) {
		out.WriteString(s + "\n")
	}, false)
	return ok, err, out.String()
}

func TestAuthenticateMCPPreflightFailures(t *testing.T) {
	const fakeAuth = `["codex-nonexistent-binary-xyz", "mcp", "login", "{target}"]`

	t.Run("server_not_configured", func(t *testing.T) {
		f := newAuthFixture(t, fakeAuth, "", true)
		_, err, _ := runAuth(f, "codex", "other-server")
		assertErr(t, err, "MCP server 'other-server' is not configured for agent 'codex'")
	})
	t.Run("disabled", func(t *testing.T) {
		f := newAuthFixture(t, fakeAuth, "\n[overrides.codex]\nenabled = false\nreason = \"managed elsewhere\"\n", true)
		_, err, _ := runAuth(f, "codex", "my-server")
		assertErr(t, err, "codex/my-server authentication is disabled: managed elsewhere")
	})
	t.Run("agent_not_detected", func(t *testing.T) {
		f := newAuthFixture(t, fakeAuth, "", false)
		if err := os.RemoveAll(filepath.Join(f.home, ".codex")); err != nil {
			t.Fatal(err)
		}
		_, err, _ := runAuth(f, "codex", "my-server")
		assertErr(t, err, "codex is not configured; run 'aikito sync mcp' first")
	})
	t.Run("config_file_missing", func(t *testing.T) {
		f := newAuthFixture(t, fakeAuth, "", false)
		_, err, _ := runAuth(f, "codex", "my-server")
		assertErr(t, err, "codex is not configured; run 'aikito sync mcp' first")
	})
	t.Run("drifted", func(t *testing.T) {
		f := newAuthFixture(t, fakeAuth, "", false)
		if err := os.WriteFile(f.configPath, []byte("[mcp_servers.my_server]\nurl = \"https://hacked.example.com/mcp\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err, _ := runAuth(f, "codex", "my-server")
		assertErr(t, err, "codex/my-server config is missing or has drifted; run 'aikito sync mcp' first")
	})
	t.Run("no_auth_command", func(t *testing.T) {
		f := newAuthFixture(t, "", "", true)
		_, err, _ := runAuth(f, "codex", "my-server")
		assertErr(t, err, "codex/my-server has no authentication command")
	})
	t.Run("cli_not_found", func(t *testing.T) {
		f := newAuthFixture(t, fakeAuth, "", true)
		_, err, out := runAuth(f, "codex", "my-server")
		assertErr(t, err, "Agent CLI not found: codex-nonexistent-binary-xyz")
		if out != "" {
			t.Errorf("no output expected before the subprocess starts, got %q", out)
		}
	})
}

func assertErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

// Real subprocess: a fake agent CLI that prints one authorization URL on
// stdout and opens a second via $BROWSER (exercising writeBrowserHelper's
// URL-file mechanism end to end).
func TestAuthenticateMCPRealSubprocess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake agent CLI")
	}
	binDir := t.TempDir()
	const printed = "https://auth.example.com/oauth/authorize?client_id=c&redirect_uri=r"
	const browsed = "https://login.example.com/authorize?client_id=x&redirect_uri=y"

	run := func(t *testing.T, script string) (bool, error, string) {
		bin := writeScript(t, binDir, fmt.Sprintf("fake-codex-%d", time.Now().UnixNano()), script)
		f := newAuthFixture(t, fmt.Sprintf(`[%q, "mcp", "login", "{target}"]`, bin), "", true)
		return runAuth(f, "codex", "my-server")
	}

	t.Run("success", func(t *testing.T) {
		ok, err, out := run(t, fmt.Sprintf(
			`echo "Logging in to $3"; echo "Open: %s"; "$BROWSER" %q; exit 0`, printed, browsed))
		if err != nil || !ok {
			t.Fatalf("ok=%v err=%v\n%s", ok, err, out)
		}
		for _, want := range []string{
			"Logging in to my_server",
			"[AUTH URL] " + printed,
			"[AUTH URL] " + browsed,
			"[SUCCESS] codex/my-server authentication completed",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q:\n%s", want, out)
			}
		}
		if strings.Count(out, "[AUTH URL] "+printed) != 1 {
			t.Errorf("authorization URL should be reported exactly once:\n%s", out)
		}
	})
	t.Run("no_url_exposed", func(t *testing.T) {
		ok, err, out := run(t, `echo "nothing to see"; exit 0`)
		if err != nil || ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
		if !strings.Contains(out, "[ERROR] Authentication command did not expose an authorization URL. No credential values were logged.") {
			t.Errorf("output:\n%s", out)
		}
	})
	t.Run("nonzero_exit", func(t *testing.T) {
		ok, err, out := run(t, fmt.Sprintf(`echo "Open: %s"; exit 2`, printed))
		if err != nil || ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
		if !strings.Contains(out, "[ERROR] Authentication command exited with status 2") {
			t.Errorf("output:\n%s", out)
		}
	})
	t.Run("sensitive_callback_url_redacted", func(t *testing.T) {
		_, _, out := run(t, fmt.Sprintf(
			`echo "callback https://cb.example.com/done?token=leaky123"; echo "Open: %s"; exit 0`, printed))
		if strings.Contains(out, "leaky123") {
			t.Errorf("sensitive callback URL leaked:\n%s", out)
		}
	})
}
