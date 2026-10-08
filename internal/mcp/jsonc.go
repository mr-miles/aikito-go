package mcp

import (
	"encoding/json"
	"strings"
)

// Token mirrors model.py's Token: a single lexed span in JSONC source text.
type Token struct {
	Kind  string // "string" | "{" | "}" | "[" | "]" | ":" | "," | "literal"
	Text  string
	Start int
	End   int
}

// Value mirrors Token.value: for a string token, the decoded (unescaped)
// string; for any other token, its resolved literal value (used only for
// scalar literal tokens by the recursive-descent parser).
func (t Token) Value() (any, error) {
	if t.Kind == "string" {
		var s string
		if err := json.Unmarshal([]byte(t.Text), &s); err != nil {
			return nil, err
		}
		return s, nil
	}
	return nil, configErrorf("Token.Value() called on non-string token %q", t.Kind)
}

// TokenizeJSONC mirrors _tokenize_jsonc: a single-pass scanner skipping
// whitespace and //.../* */ comments, lexing JSON strings (backslash
// blindly escapes the next character, matching Python's behavior exactly,
// including its EOF-overrun case), single-char structural tokens, and
// otherwise greedy "literal" runs (numbers, true/false/null).
func TokenizeJSONC(text string) ([]Token, error) {
	var tokens []Token
	index := 0
	n := len(text)
	isSpace := func(b byte) bool {
		return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
	}
	for index < n {
		c := text[index]
		if isSpace(c) {
			index++
			continue
		}
		if strings.HasPrefix(text[index:], "//") {
			nl := strings.IndexByte(text[index+2:], '\n')
			if nl == -1 {
				index = n
			} else {
				index = index + 2 + nl + 1
			}
			continue
		}
		if strings.HasPrefix(text[index:], "/*") {
			end := strings.Index(text[index+2:], "*/")
			if end == -1 {
				return nil, configErrorf("Unterminated JSONC block comment")
			}
			index = index + 2 + end + 2
			continue
		}
		if c == '"' {
			start := index
			index++
			closed := false
			for index < n {
				if text[index] == '\\' {
					index += 2
					continue
				}
				if text[index] == '"' {
					index++
					closed = true
					break
				}
				index++
			}
			if !closed {
				return nil, configErrorf("Unterminated JSONC string")
			}
			tokens = append(tokens, Token{"string", text[start:index], start, index})
			continue
		}
		if strings.ContainsRune("{}[]:,", rune(c)) {
			tokens = append(tokens, Token{string(c), string(c), index, index + 1})
			index++
			continue
		}
		start := index
		for index < n && !isSpace(text[index]) && !strings.ContainsRune("{}[]:,/", rune(text[index])) {
			index++
		}
		if start == index {
			return nil, configErrorf("Unexpected JSONC character at offset %d", index)
		}
		tokens = append(tokens, Token{"literal", text[start:index], start, index})
	}
	return tokens, nil
}

// parseJSONValue mirrors _parse_json_value: recursive-descent over the
// token list, building *OrderedObject for objects (duplicate keys: last
// value wins, original position kept — Python dict assignment semantics,
// not the duplicate-rejecting strict decoder used elsewhere in this port).
func parseJSONValue(tokens []Token, index int) (any, int, error) {
	if index >= len(tokens) {
		return nil, 0, configErrorf("Unexpected end of JSONC input")
	}
	tok := tokens[index]
	switch tok.Kind {
	case "string":
		v, err := tok.Value()
		if err != nil {
			return nil, 0, err
		}
		return v, index + 1, nil
	case "{":
		result := NewOrderedObject()
		index++
		for index < len(tokens) && tokens[index].Kind != "}" {
			keyTok := tokens[index]
			if keyTok.Kind != "string" {
				return nil, 0, configErrorf("JSONC object keys must be strings")
			}
			if index+1 >= len(tokens) || tokens[index+1].Kind != ":" {
				return nil, 0, configErrorf("JSONC object key is missing ':'")
			}
			var value any
			var err error
			value, index, err = parseJSONValue(tokens, index+2)
			if err != nil {
				return nil, 0, err
			}
			keyVal, _ := keyTok.Value()
			result.Set(keyVal.(string), value)
			if index < len(tokens) && tokens[index].Kind == "," {
				index++
			}
		}
		if index >= len(tokens) || tokens[index].Kind != "}" {
			return nil, 0, configErrorf("Unterminated JSONC object")
		}
		return result, index + 1, nil
	case "[":
		result := []any{}
		index++
		for index < len(tokens) && tokens[index].Kind != "]" {
			var value any
			var err error
			value, index, err = parseJSONValue(tokens, index)
			if err != nil {
				return nil, 0, err
			}
			result = append(result, value)
			if index < len(tokens) && tokens[index].Kind == "," {
				index++
			}
		}
		if index >= len(tokens) || tokens[index].Kind != "]" {
			return nil, 0, configErrorf("Unterminated JSONC array")
		}
		return result, index + 1, nil
	default:
		switch tok.Text {
		case "true":
			return true, index + 1, nil
		case "false":
			return false, index + 1, nil
		case "null":
			return nil, index + 1, nil
		}
		var num json.Number
		dec := json.NewDecoder(strings.NewReader(tok.Text))
		dec.UseNumber()
		if err := dec.Decode(&num); err != nil {
			return nil, 0, configErrorf("Invalid JSONC literal: %s", tok.Text)
		}
		return jsonNumberToGo(num), index + 1, nil
	}
}

// ParseJSONC mirrors _parse_jsonc/parse_jsonc: an empty token stream parses
// as an empty object (matching Python's `if not tokens: return {}`).
func ParseJSONC(text string) (any, error) {
	tokens, err := TokenizeJSONC(text)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return NewOrderedObject(), nil
	}
	value, next, err := parseJSONValue(tokens, 0)
	if err != nil {
		return nil, err
	}
	if next != len(tokens) {
		return nil, configErrorf("Unexpected content after JSONC document")
	}
	return value, nil
}

// objectMember records one object member's token-index span: (keyIndex,
// valueStartIndex, valueEndIndex-inclusive) — mirrors _object_members'
// per-key tuple, used by the surgical splicer to replace/insert exact text
// spans without touching anything else.
type objectMember struct {
	keyIndex   int
	valueStart int
	valueEnd   int // inclusive, last token index belonging to the value
}

// objectMembers mirrors _object_members.
func objectMembers(tokens []Token, objectIndex int) (map[string]objectMember, int, error) {
	if tokens[objectIndex].Kind != "{" {
		return nil, 0, configErrorf("Expected a JSONC object")
	}
	members := map[string]objectMember{}
	index := objectIndex + 1
	for index < len(tokens) && tokens[index].Kind != "}" {
		keyIndex := index
		if tokens[keyIndex].Kind != "string" {
			return nil, 0, configErrorf("JSONC object keys must be strings")
		}
		if keyIndex+1 >= len(tokens) || tokens[keyIndex+1].Kind != ":" {
			return nil, 0, configErrorf("JSONC object key is missing ':'")
		}
		valueIndex := keyIndex + 2
		_, nextIndex, err := parseJSONValue(tokens, valueIndex)
		if err != nil {
			return nil, 0, err
		}
		keyVal, _ := tokens[keyIndex].Value()
		members[keyVal.(string)] = objectMember{keyIndex, valueIndex, nextIndex - 1}
		index = nextIndex
		if index < len(tokens) && tokens[index].Kind == "," {
			index++
		}
	}
	if index >= len(tokens) || tokens[index].Kind != "}" {
		return nil, 0, configErrorf("Unterminated JSONC object")
	}
	return members, index, nil
}

// lineIndent mirrors _line_indent: the raw leading whitespace of the line
// containing offset (works with mixed tabs/spaces, no consistent-indent
// assumption).
func lineIndent(text string, offset int) string {
	lineStart := strings.LastIndexByte(text[:offset], '\n') + 1
	return text[lineStart:offset]
}

// formatJSONValue mirrors _format_json_value: json.dumps(value, indent=2),
// then every line after the first is prefixed with indent so the value
// block's continuation lines align under the caller's placement.
func formatJSONValue(value any, indent string) string {
	dumped := DumpIndented(value)
	lines := strings.Split(dumped, "\n")
	var b strings.Builder
	b.WriteString(lines[0])
	for _, line := range lines[1:] {
		b.WriteString("\n")
		b.WriteString(indent)
		b.WriteString(line)
	}
	return b.String()
}

var jsonConfigDisplayNames = map[string]string{
	"agy_json":     "agy",
	"claude_json":  "Claude Code",
	"copilot_json": "GitHub Copilot CLI",
}

// LoadDocument mirrors _load_document's format dispatch. text.strip()==""
// short-circuits to an empty object for every format, matching Python.
func LoadDocument(configFormat, text string) (any, error) {
	if strings.TrimSpace(text) == "" {
		return NewOrderedObject(), nil
	}
	switch configFormat {
	case "toml":
		doc, err := DecodeTOMLOrdered(text)
		if err != nil {
			return nil, configErrorf("Invalid Codex TOML config: %v", err)
		}
		return doc, nil
	case "jsonc":
		doc, err := ParseJSONC(text)
		if err != nil {
			return nil, err
		}
		if _, ok := doc.(*OrderedObject); !ok {
			return nil, configErrorf("OpenCode config root must be an object")
		}
		return doc, nil
	case "agy_json", "claude_json", "copilot_json":
		configName := jsonConfigDisplayNames[configFormat]
		doc, err := ParseJSONOrdered(text)
		if err != nil {
			// Report the error the way json.loads does, where it can be.
			if msg := PythonJSONDecodeError(text); msg != "" {
				return nil, configErrorf("Invalid %s JSON config: %s", configName, msg)
			}
			return nil, configErrorf("Invalid %s JSON config: %v", configName, err)
		}
		if _, ok := doc.(*OrderedObject); !ok {
			return nil, configErrorf("%s config root must be an object", configName)
		}
		return doc, nil
	case "dsh_cordis":
		entries, err := ParseDSHCordisEntries(text)
		if err != nil {
			return nil, err
		}
		root := NewOrderedObject()
		root.Set("mcpServers", entries)
		return root, nil
	default:
		return nil, configErrorf("Unsupported config format: %s", configFormat)
	}
}

// GetJSONCServer mirrors get_jsonc_server.
func GetJSONCServer(text, serverName string) (*OrderedObject, error) {
	doc, err := LoadDocument("jsonc", text)
	if err != nil {
		return nil, err
	}
	root := doc.(*OrderedObject)
	mcpVal, ok := root.Get("mcp")
	if !ok || mcpVal == nil {
		return nil, nil
	}
	mcp, ok := mcpVal.(*OrderedObject)
	if !ok {
		return nil, configErrorf("OpenCode 'mcp' must be an object")
	}
	serverVal, ok := mcp.Get(serverName)
	if !ok || serverVal == nil {
		return nil, nil
	}
	server, ok := serverVal.(*OrderedObject)
	if !ok {
		return nil, configErrorf("OpenCode MCP server '%s' must be an object", serverName)
	}
	return server, nil
}

// GetMCPJSONServer mirrors get_mcp_json_server (shared by agy/claude/copilot).
func GetMCPJSONServer(text, serverName, configFormat, configName string) (*OrderedObject, error) {
	doc, err := LoadDocument(configFormat, text)
	if err != nil {
		return nil, err
	}
	root := doc.(*OrderedObject)
	serversVal := root.GetOr("mcpServers", NewOrderedObject())
	servers, ok := serversVal.(*OrderedObject)
	if !ok {
		return nil, configErrorf("%s 'mcpServers' must be an object", configName)
	}
	serverVal, ok := servers.Get(serverName)
	if !ok || serverVal == nil {
		return nil, nil
	}
	server, ok := serverVal.(*OrderedObject)
	if !ok {
		return nil, configErrorf("%s MCP server '%s' must be an object", configName, serverName)
	}
	return server, nil
}

func GetAgyJSONServer(text, serverName string) (*OrderedObject, error) {
	return GetMCPJSONServer(text, serverName, "agy_json", "agy")
}
func GetClaudeJSONServer(text, serverName string) (*OrderedObject, error) {
	return GetMCPJSONServer(text, serverName, "claude_json", "Claude Code")
}
func GetCopilotJSONServer(text, serverName string) (*OrderedObject, error) {
	return GetMCPJSONServer(text, serverName, "copilot_json", "GitHub Copilot CLI")
}

// UpdateMCPJSONServer mirrors update_mcp_json_server: full parse-mutate-
// reserialize (safe for plain .json files, no comments to preserve).
func UpdateMCPJSONServer(text, serverName string, desired *OrderedObject, configFormat, configName string) (string, error) {
	doc, err := LoadDocument(configFormat, text)
	if err != nil {
		return "", err
	}
	root := doc.(*OrderedObject)
	serversVal, has := root.Get("mcpServers")
	var servers *OrderedObject
	if !has {
		servers = NewOrderedObject()
		root.Set("mcpServers", servers)
	} else {
		servers, _ = serversVal.(*OrderedObject)
		if servers == nil {
			return "", configErrorf("%s 'mcpServers' must be an object", configName)
		}
	}
	servers.Set(serverName, desired)
	return DumpIndented(root) + "\n", nil
}

func UpdateAgyJSONServer(text, serverName string, desired *OrderedObject) (string, error) {
	return UpdateMCPJSONServer(text, serverName, desired, "agy_json", "agy")
}
func UpdateClaudeJSONServer(text, serverName string, desired *OrderedObject) (string, error) {
	return UpdateMCPJSONServer(text, serverName, desired, "claude_json", "Claude Code")
}
func UpdateCopilotJSONServer(text, serverName string, desired *OrderedObject) (string, error) {
	return UpdateMCPJSONServer(text, serverName, desired, "copilot_json", "GitHub Copilot CLI")
}

// RemoveMCPJSONServer mirrors remove_mcp_json_server.
func RemoveMCPJSONServer(text, serverName, configFormat, configName string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return text, nil
	}
	doc, err := LoadDocument(configFormat, text)
	if err != nil {
		return "", err
	}
	root := doc.(*OrderedObject)
	serversVal, _ := root.Get("mcpServers")
	servers, ok := serversVal.(*OrderedObject)
	if ok && servers.Has(serverName) {
		servers.Delete(serverName)
		return DumpIndented(root) + "\n", nil
	}
	return text, nil
}

func RemoveAgyJSONServer(text, serverName string) (string, error) {
	return RemoveMCPJSONServer(text, serverName, "agy_json", "agy")
}
func RemoveClaudeJSONServer(text, serverName string) (string, error) {
	return RemoveMCPJSONServer(text, serverName, "claude_json", "Claude Code")
}
func RemoveCopilotJSONServer(text, serverName string) (string, error) {
	return RemoveMCPJSONServer(text, serverName, "copilot_json", "GitHub Copilot CLI")
}

// UpdateJSONCServer mirrors update_jsonc_server: surgical token-span
// splicing so comments/formatting/sibling servers are preserved exactly.
func UpdateJSONCServer(text, serverName string, desired *OrderedObject) (string, error) {
	if strings.TrimSpace(text) == "" {
		text = "{}\n"
	}
	tokens, err := TokenizeJSONC(text)
	if err != nil {
		return "", err
	}
	if len(tokens) == 0 || tokens[0].Kind != "{" {
		return "", configErrorf("OpenCode config root must be an object")
	}
	rootMembers, rootClose, err := objectMembers(tokens, 0)
	if err != nil {
		return "", err
	}
	mcpMember, hasMCP := rootMembers["mcp"]
	if !hasMCP {
		rootIndent := lineIndent(text, tokens[0].Start)
		childIndent := rootIndent + "  "
		serverIndent := childIndent + "  "
		formatted := formatJSONValue(desired, serverIndent)
		var keyBuf strings.Builder
		writeJSONStringPy(&keyBuf, serverName)
		propertyText := childIndent + `"mcp": {` + "\n" +
			serverIndent + keyBuf.String() + ": " + formatted + "\n" +
			childIndent + "}"
		previousToken := tokens[rootClose-1]
		prefix := ",\n"
		if previousToken.Kind == "," || len(rootMembers) == 0 {
			prefix = "\n"
		}
		suffix := "\n" + rootIndent
		closeOffset := tokens[rootClose].Start
		return text[:closeOffset] + prefix + propertyText + suffix + text[closeOffset:], nil
	}

	mcpValueIndex := mcpMember.valueStart
	if tokens[mcpValueIndex].Kind != "{" {
		return "", configErrorf("OpenCode 'mcp' must be an object")
	}
	mcpMembers, mcpClose, err := objectMembers(tokens, mcpValueIndex)
	if err != nil {
		return "", err
	}
	if serverMember, ok := mcpMembers[serverName]; ok {
		indent := lineIndent(text, tokens[serverMember.keyIndex].Start)
		formatted := formatJSONValue(desired, indent)
		start := tokens[serverMember.valueStart].Start
		end := tokens[serverMember.valueEnd].End
		return text[:start] + formatted + text[end:], nil
	}

	mcpKeyIndex := mcpMember.keyIndex
	mcpIndent := lineIndent(text, tokens[mcpKeyIndex].Start)
	childIndent := mcpIndent + "  "
	formatted := formatJSONValue(desired, childIndent)
	var keyBuf strings.Builder
	writeJSONStringPy(&keyBuf, serverName)
	propertyText := childIndent + keyBuf.String() + ": " + formatted
	closeOffset := tokens[mcpClose].Start
	previousToken := tokens[mcpClose-1]
	separator := ",\n"
	if previousToken.Kind == "," || len(mcpMembers) == 0 {
		separator = "\n"
	}
	return text[:closeOffset] + separator + propertyText + "\n" + mcpIndent + text[closeOffset:], nil
}

// RemoveJSONCServer mirrors remove_jsonc_server: locate the member's token
// span, decide whether to also eat a trailing or leading comma + owning
// whitespace, splice it out, then re-parse as a correctness check.
func RemoveJSONCServer(text, serverName string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return text, nil
	}
	tokens, err := TokenizeJSONC(text)
	if err != nil {
		return "", err
	}
	if len(tokens) == 0 || tokens[0].Kind != "{" {
		return "", configErrorf("OpenCode config root must be an object")
	}
	rootMembers, _, err := objectMembers(tokens, 0)
	if err != nil {
		return "", err
	}
	mcpMember, hasMCP := rootMembers["mcp"]
	if !hasMCP {
		return text, nil
	}
	mcpValueIndex := mcpMember.valueStart
	if tokens[mcpValueIndex].Kind != "{" {
		return "", configErrorf("OpenCode 'mcp' must be an object")
	}
	mcpMembers, mcpClose, err := objectMembers(tokens, mcpValueIndex)
	if err != nil {
		return "", err
	}
	serverMember, ok := mcpMembers[serverName]
	if !ok {
		return text, nil
	}

	keyIdx := serverMember.keyIndex
	valEndIdx := serverMember.valueEnd

	hasTrailingComma := valEndIdx+1 < mcpClose && tokens[valEndIdx+1].Kind == ","
	hasLeadingComma := keyIdx-1 > mcpValueIndex && tokens[keyIdx-1].Kind == ","

	cutStart := tokens[keyIdx].Start
	lineStart := strings.LastIndexByte(text[:cutStart], '\n')
	if lineStart != -1 && strings.TrimSpace(text[lineStart+1:cutStart]) == "" {
		cutStart = lineStart + 1
	}

	var cutEnd int
	if hasTrailingComma {
		commaTok := tokens[valEndIdx+1]
		cutEnd = commaTok.End
		cutEnd = advancePastNewline(text, cutEnd)
	} else if hasLeadingComma {
		commaTok := tokens[keyIdx-1]
		cutStart = commaTok.Start
		cutEnd = tokens[valEndIdx].End
		cutEnd = advancePastNewline(text, cutEnd)
	} else {
		cutEnd = tokens[valEndIdx].End
		cutEnd = advancePastNewline(text, cutEnd)
	}

	result := text[:cutStart] + text[cutEnd:]
	parsed, err := ParseJSONC(result)
	if err != nil {
		return "", err
	}
	parsedRoot, _ := parsed.(*OrderedObject)
	mcpVal := parsedRoot.GetOr("mcp", NewOrderedObject())
	if mcpObj, ok := mcpVal.(*OrderedObject); ok && mcpObj.Has(serverName) {
		return "", configErrorf("Failed to remove server '%s' from OpenCode JSONC", serverName)
	}
	return result, nil
}

func advancePastNewline(text string, offset int) int {
	if offset < len(text) && text[offset] == '\n' {
		return offset + 1
	}
	if offset+1 < len(text) && text[offset:offset+2] == "\r\n" {
		return offset + 2
	}
	return offset
}
