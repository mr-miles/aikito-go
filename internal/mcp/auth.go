package mcp

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// authorizationLabel mirrors _authorization_label.
func authorizationLabel(value, source string) string {
	scheme := ""
	if strings.TrimSpace(value) != "" {
		fields := strings.Fields(value)
		scheme = titleCase(fields[0])
	}
	if scheme != "Basic" && scheme != "Bearer" {
		if source == "env" {
			return "Environment header"
		}
		return "Unknown"
	}
	suffix := "inline header"
	if source == "env" {
		suffix = "env header"
	}
	return scheme + " · " + suffix
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
}

// DescribeMCPAuth mirrors describe_mcp_auth: a best-effort, read-only
// classification of an entry's configured auth, for display only.
func DescribeMCPAuth(entry *OrderedObject) string {
	if entry == nil {
		return "None"
	}
	if envHeadersVal, ok := entry.Get("env_http_headers"); ok {
		if envHeaders, ok := envHeadersVal.(*OrderedObject); ok {
			for _, name := range envHeaders.Keys() {
				if !strings.EqualFold(name, "authorization") {
					continue
				}
				envNameVal, _ := envHeaders.Get(name)
				envName, ok := envNameVal.(string)
				if !ok {
					continue
				}
				return authorizationLabel(os.Getenv(envName), "env")
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
			if !strings.EqualFold(name, "authorization") {
				continue
			}
			rawVal, _ := headers.Get(name)
			rawValue, ok := rawVal.(string)
			if !ok {
				continue
			}
			envName := EnvironmentReference(rawValue)
			value := rawValue
			source := "inline"
			if envName != "" {
				value = os.Getenv(envName)
				source = "env"
			}
			return authorizationLabel(value, source)
		}
	}

	if bearerVal, ok := entry.Get("bearer_token_env_var"); ok {
		if bearerEnv, ok := bearerVal.(string); ok && bearerEnv != "" {
			return "Bearer · env token"
		}
	}
	if authVal, ok := entry.Get("auth"); ok {
		if s, ok := authVal.(string); ok && s == "oauth" {
			return "OAuth"
		}
	}
	if oauthVal, ok := entry.Get("oauth"); ok {
		if b, ok := oauthVal.(bool); ok && b {
			return "OAuth"
		}
	}
	return "None"
}

// writeBrowserHelper mirrors _write_browser_helper: a tiny shell script (not
// Python, unlike the original — this Go port has no Python runtime to shell
// out to) that intercepts any URL the agent CLI's OAuth flow tries to open
// via $BROWSER, appending http(s) argv entries to AIKITO_AUTH_URL_FILE and
// optionally invoking the platform opener when AIKITO_OPEN_BROWSER=1.
func writeBrowserHelper(dir string) (string, error) {
	if runtime.GOOS == "windows" {
		script := `@echo off
setlocal enabledelayedexpansion
if not "%AIKITO_AUTH_URL_FILE%"=="" (
  for %%u in (%*) do (
    set "arg=%%u"
    if "!arg:~0,7!"=="http://" ( echo %%u>>"%AIKITO_AUTH_URL_FILE%" )
    if "!arg:~0,8!"=="https://" ( echo %%u>>"%AIKITO_AUTH_URL_FILE%" )
  )
)
if "%AIKITO_OPEN_BROWSER%"=="1" (
  for %%u in (%*) do ( start "" "%%u" )
)
`
		helper := filepath.Join(dir, "aikito-browser.cmd")
		if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
			return "", err
		}
		return helper, nil
	}

	script := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    http://*|https://*)
      if [ -n "$AIKITO_AUTH_URL_FILE" ]; then
        printf '%s\n' "$arg" >> "$AIKITO_AUTH_URL_FILE"
      fi
      if [ "$AIKITO_OPEN_BROWSER" = "1" ]; then
        if command -v xdg-open >/dev/null 2>&1; then
          xdg-open "$arg" >/dev/null 2>&1 &
        elif [ "$(uname)" = "Darwin" ] && [ -x /usr/bin/open ]; then
          /usr/bin/open "$arg" >/dev/null 2>&1 &
        fi
      fi
      ;;
  esac
done
`
	helper := filepath.Join(dir, "aikito-browser")
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		return "", err
	}
	return helper, nil
}

// AuthenticateMCP mirrors authenticate_mcp: drives the agent's own
// interactive auth CLI command as a real subprocess, surfacing any OAuth
// authorization URL it prints or tries to open, so the user (or an
// automation) can complete the flow. Returns (true, nil) only if at least
// one authorization URL was observed AND the process exited 0.
func AuthenticateMCP(aikitoDir, home, agent, server string, output func(string), openBrowser bool) (bool, error) {
	if output == nil {
		output = func(string) {}
	}
	specs, err := LoadAgentSpecs(aikitoDir, home)
	if err != nil {
		return false, err
	}
	var spec *AgentSpec
	for i := range specs {
		if specs[i].Agent == agent && specs[i].Server == server {
			spec = &specs[i]
			break
		}
	}
	if spec == nil {
		return false, configErrorf("MCP server '%s' is not configured for agent '%s'", server, agent)
	}
	if !spec.Enabled {
		return false, configErrorf("%s/%s authentication is disabled: %s", agent, server, spec.Reason)
	}
	if !AgentDetected(*spec) {
		return false, configErrorf("%s is not configured; run 'aikito sync mcp' first", agent)
	}
	if _, err := os.Stat(spec.ConfigPath); err != nil {
		return false, configErrorf("%s is not configured; run 'aikito sync mcp' first", agent)
	}
	text, err := readFileString(spec.ConfigPath)
	if err != nil {
		return false, err
	}
	current, err := ReadEntry(*spec, text)
	if err != nil {
		return false, err
	}
	if !EntryMatchesDesired(*spec, current) {
		return false, configErrorf("%s/%s config is missing or has drifted; run 'aikito sync mcp' first", agent, server)
	}
	if len(spec.AuthCommand) == 0 {
		return false, configErrorf("%s/%s has no authentication command", agent, server)
	}
	if _, err := exec.LookPath(spec.AuthCommand[0]); err != nil {
		return false, configErrorf("Agent CLI not found: %s", spec.AuthCommand[0])
	}

	output("[AUTH] " + strings.Join(spec.AuthCommand, " "))

	tmpDir, err := os.MkdirTemp("", "aikito-mcp-auth-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmpDir)

	urlFile := filepath.Join(tmpDir, "authorization-urls")
	browserHelper, err := writeBrowserHelper(tmpDir)
	if err != nil {
		return false, err
	}

	cmd := exec.Command(spec.AuthCommand[0], spec.AuthCommand[1:]...)
	cmd.Env = append(os.Environ(),
		"AIKITO_AUTH_URL_FILE="+urlFile,
		"AIKITO_OPEN_BROWSER="+boolEnvStr(openBrowser),
		"BROWSER="+browserHelper,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	cmd.Stderr = cmd.Stdout // combined, matching stderr=subprocess.STDOUT

	if err := cmd.Start(); err != nil {
		return false, err
	}

	seenURLs := map[string]bool{}
	lineCh := make(chan string)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		close(lineCh)
	}()

	emitURL := func(url string) {
		if IsAuthorizationURL(url) && !seenURLs[url] {
			seenURLs[url] = true
			output("[AUTH URL] " + url)
		}
	}
	checkURLFile := func() {
		data, err := os.ReadFile(urlFile)
		if err != nil || len(data) == 0 || !strings.HasSuffix(string(data), "\n") {
			return
		}
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			emitURL(line)
		}
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	linesOpen := true
	for linesOpen {
		select {
		case line, ok := <-lineCh:
			if !ok {
				linesOpen = false
				continue
			}
			output(RedactSensitiveURLs(line))
			for _, url := range URLsInText(line) {
				emitURL(url)
			}
		case <-ticker.C:
			checkURLFile()
		}
	}
	checkURLFile()

	err = cmd.Wait()
	returnCode := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		returnCode = exitErr.ExitCode()
	} else if err != nil {
		returnCode = -1
	}

	if len(seenURLs) == 0 {
		output("[ERROR] Authentication command did not expose an authorization URL. No credential values were logged.")
		return false, nil
	}
	if returnCode != 0 {
		output(fmt.Sprintf("[ERROR] Authentication command exited with status %d", returnCode))
		return false, nil
	}
	output(fmt.Sprintf("[SUCCESS] %s/%s authentication completed", agent, server))
	return true, nil
}

func boolEnvStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
