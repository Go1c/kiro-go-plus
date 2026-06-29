package proxy

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestExtractOpenAIMessageTextStructured(t *testing.T) {
	content := []interface{}{
		map[string]interface{}{"type": "text", "text": "alpha"},
		map[string]interface{}{"type": "input_text", "text": "beta"},
	}

	if got := extractOpenAIMessageText(content); got != "alphabeta" {
		t.Fatalf("expected concatenated structured text, got %q", got)
	}

	nested := map[string]interface{}{
		"content": []interface{}{map[string]interface{}{"type": "text", "text": "nested"}},
	}
	if got := extractOpenAIMessageText(nested); got != "nested" {
		t.Fatalf("expected nested content extraction, got %q", got)
	}
}

func TestOpenAIToKiroPreservesStructuredAssistantAndToolContent(t *testing.T) {
	req := &OpenAIRequest{
		Model: "claude-sonnet-4.5",
		Messages: []OpenAIMessage{
			{
				Role: "system",
				Content: []interface{}{
					map[string]interface{}{"type": "text", "text": "system-a"},
					map[string]interface{}{"type": "text", "text": "system-b"},
				},
			},
			{Role: "user", Content: "first-question"},
			{
				Role: "assistant",
				Content: []interface{}{
					map[string]interface{}{"type": "text", "text": "assistant-structured"},
				},
			},
			{
				Role:       "tool",
				ToolCallID: "call_1",
				Content: []interface{}{
					map[string]interface{}{"type": "text", "text": "tool-result-structured"},
				},
			},
		},
	}

	payload := OpenAIToKiro(req, false)

	// History starts with a priming pair.
	if len(payload.ConversationState.History) != 4 {
		t.Fatalf("expected 4 history items (2 priming + 2 conversation), got %d", len(payload.ConversationState.History))
	}

	// history[0]: priming user
	primingUser := payload.ConversationState.History[0].UserInputMessage
	if primingUser == nil {
		t.Fatalf("expected history[0] to be priming user message")
	}
	if !strings.Contains(primingUser.Content, "system-a") || !strings.Contains(primingUser.Content, "system-b") {
		t.Fatalf("expected priming user message to contain system prompt, got %q", primingUser.Content)
	}
	if strings.Contains(primingUser.Content, "first-question") {
		t.Fatalf("expected system prompt priming not to contain user question, got %q", primingUser.Content)
	}

	// history[1]: priming assistant
	primingAssistant := payload.ConversationState.History[1].AssistantResponseMessage
	if primingAssistant == nil {
		t.Fatalf("expected history[1] to be priming assistant message")
	}
	if primingAssistant.Content != "I will follow these instructions." {
		t.Fatalf("expected priming assistant ack, got %q", primingAssistant.Content)
	}

	// history[2]: first user turn
	firstConvUser := payload.ConversationState.History[2].UserInputMessage
	if firstConvUser == nil {
		t.Fatalf("expected history[2] to be first conversation user message")
	}
	if !strings.Contains(firstConvUser.Content, "first-question") {
		t.Fatalf("expected history[2] to contain first-question, got %q", firstConvUser.Content)
	}

	// history[3]: assistant reply
	historyAssistant := payload.ConversationState.History[3].AssistantResponseMessage
	if historyAssistant == nil {
		t.Fatalf("expected history[3] to be assistant message")
	}
	if historyAssistant.Content != "assistant-structured" {
		t.Fatalf("expected assistant structured content to be preserved, got %q", historyAssistant.Content)
	}

	// The tool result answers call_1, but the last history assistant has no
	// matching structured tool call (it is text-only), so the tool result is an
	// orphan. Kiro's upstream rejects structured tool results that do not answer
	// the immediately preceding assistant tool call, so it must be narrated into
	// the current message text rather than kept structured.
	cur := payload.ConversationState.CurrentMessage.UserInputMessage
	if !strings.Contains(cur.Content, "tool-result-structured") {
		t.Fatalf("expected tool-result continuation content, got %q", cur.Content)
	}
	if cur.UserInputMessageContext != nil && len(cur.UserInputMessageContext.ToolResults) != 0 {
		t.Fatalf("expected orphan tool result to be flattened into text, not kept structured")
	}
}

func TestOpenAIToKiroAssistantMapContentInHistory(t *testing.T) {
	req := &OpenAIRequest{
		Model: "claude-sonnet-4.5",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "u1"},
			{Role: "assistant", Content: map[string]interface{}{"type": "text", "text": "assistant-map"}},
			{Role: "user", Content: "u2"},
		},
	}

	payload := OpenAIToKiro(req, false)

	if len(payload.ConversationState.History) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(payload.ConversationState.History))
	}
	assistant := payload.ConversationState.History[1].AssistantResponseMessage
	if assistant == nil {
		t.Fatalf("expected second history entry to be assistant")
	}
	if assistant.Content != "assistant-map" {
		t.Fatalf("expected assistant map content preserved, got %q", assistant.Content)
	}
}

func TestOpenAIToKiroAssistantToolCallsDoNotInjectPlaceholder(t *testing.T) {
	req := &OpenAIRequest{
		Model: "claude-sonnet-4.5",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "find weather"},
			{
				Role:    "assistant",
				Content: nil,
				ToolCalls: []ToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "get_weather", Arguments: "{}"},
				}},
			},
			{Role: "user", Content: "continue"},
		},
	}

	payload := OpenAIToKiro(req, false)

	// The mid-history assistant turn carried ONLY a tool call (no text) and is
	// not the active tool turn, so its structured toolUses are cleared. That
	// leaves it hollow, and a hollow assistant turn is dropped entirely rather
	// than backfilled with a "." placeholder (which the model would imitate).
	// No surviving turn may contain tool-invocation text or structured toolUses.
	for i, h := range payload.ConversationState.History {
		a := h.AssistantResponseMessage
		if a == nil {
			continue
		}
		if len(a.ToolUses) != 0 {
			t.Fatalf("history[%d] retains structured toolUses", i)
		}
		if strings.Contains(a.Content, "get_weather") || strings.Contains(a.Content, "[Called tool") {
			t.Fatalf("history[%d] assistant contains tool-invocation text: %q", i, a.Content)
		}
		if strings.TrimSpace(a.Content) == "." || strings.TrimSpace(a.Content) == "" {
			t.Fatalf("history[%d] is a hollow assistant turn that should have been dropped", i)
		}
	}
}

func TestOpenAIConversationIDStableFromAnchor(t *testing.T) {
	baseMessages := []OpenAIMessage{
		{Role: "system", Content: "You are helpful"},
		{Role: "user", Content: "Build calculator"},
		{Role: "assistant", Content: "Sure"},
		{Role: "user", Content: "Continue"},
	}

	reqA := &OpenAIRequest{Model: "claude-sonnet-4.5", Messages: baseMessages}
	reqB := &OpenAIRequest{Model: "claude-sonnet-4.5", Messages: append(baseMessages, OpenAIMessage{Role: "assistant", Content: "Next step"})}

	payloadA := OpenAIToKiro(reqA, false)
	payloadB := OpenAIToKiro(reqB, false)

	if payloadA.ConversationState.ConversationID == "" || payloadB.ConversationState.ConversationID == "" {
		t.Fatalf("expected non-empty conversation IDs")
	}
	if payloadA.ConversationState.ConversationID != payloadB.ConversationState.ConversationID {
		t.Fatalf("expected stable conversation ID across turns, got %q vs %q", payloadA.ConversationState.ConversationID, payloadB.ConversationState.ConversationID)
	}
}

func TestClaudeConversationIDStableFromAnchor(t *testing.T) {
	reqA := &ClaudeRequest{
		Model:  "claude-sonnet-4.5",
		System: "sys",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "hello"},
		},
	}
	reqB := &ClaudeRequest{
		Model:  "claude-sonnet-4.5",
		System: "sys",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "ok"},
			{Role: "user", Content: "next"},
		},
	}

	payloadA := ClaudeToKiro(reqA, false)
	payloadB := ClaudeToKiro(reqB, false)

	if payloadA.ConversationState.ConversationID == "" || payloadB.ConversationState.ConversationID == "" {
		t.Fatalf("expected non-empty conversation IDs")
	}
	if payloadA.ConversationState.ConversationID != payloadB.ConversationState.ConversationID {
		t.Fatalf("expected stable conversation ID across turns, got %q vs %q", payloadA.ConversationState.ConversationID, payloadB.ConversationState.ConversationID)
	}
}

func TestOpenAIConversationIDRandomForSyntheticAnchor(t *testing.T) {
	req := &OpenAIRequest{
		Model: "claude-sonnet-4.5",
		Messages: []OpenAIMessage{
			{Role: "assistant", Content: "prefill"},
		},
	}

	payloadA := OpenAIToKiro(req, false)
	payloadB := OpenAIToKiro(req, false)

	if payloadA.ConversationState.ConversationID == payloadB.ConversationState.ConversationID {
		t.Fatalf("expected synthetic anchor to generate non-deterministic conversation IDs")
	}
}

func TestClaudeToKiroDropsLeadingAssistantHistory(t *testing.T) {
	req := &ClaudeRequest{
		Model: "claude-sonnet-4.5",
		Messages: []ClaudeMessage{
			{Role: "assistant", Content: "prefill"},
			{Role: "user", Content: "real user message"},
		},
	}

	payload := ClaudeToKiro(req, false)

	if len(payload.ConversationState.History) != 0 {
		t.Fatalf("expected leading assistant-only history to be dropped, got %d entries", len(payload.ConversationState.History))
	}

	if strings.Contains(payload.ConversationState.CurrentMessage.UserInputMessage.Content, "Begin conversation") {
		t.Fatalf("unexpected synthetic Begin conversation injection in current content: %q", payload.ConversationState.CurrentMessage.UserInputMessage.Content)
	}
}

func TestKiroToClaudeResponseCanEmitEmptyThinkingBlock(t *testing.T) {
	resp := KiroToClaudeResponse("final answer", "", true, nil, 10, 20, "claude-sonnet-4.6")

	if len(resp.Content) != 2 {
		t.Fatalf("expected empty thinking block plus text block, got %d blocks", len(resp.Content))
	}
	if resp.Content[0].Type != "thinking" {
		t.Fatalf("expected first block to be thinking, got %#v", resp.Content[0])
	}
	if resp.Content[0].Thinking != "" {
		t.Fatalf("expected omitted thinking block to have empty content, got %#v", resp.Content[0].Thinking)
	}
	if resp.Content[0].Signature == "" {
		t.Fatalf("expected thinking block to include a signature")
	}
	if resp.Content[1].Type != "text" || resp.Content[1].Text != "final answer" {
		t.Fatalf("expected text block to be preserved, got %#v", resp.Content[1])
	}
}

func TestKiroToClaudeResponseThinkingBlockIncludesSignature(t *testing.T) {
	resp := KiroToClaudeResponse("final answer", "private reasoning", false, nil, 10, 20, "claude-opus-4.8")

	if len(resp.Content) != 2 {
		t.Fatalf("expected thinking and text blocks, got %d", len(resp.Content))
	}
	if resp.Content[0].Type != "thinking" || resp.Content[0].Thinking != "private reasoning" {
		t.Fatalf("expected thinking block first, got %#v", resp.Content[0])
	}
	if !strings.HasPrefix(resp.Content[0].Signature, "EqQBCgIYAhIM") {
		t.Fatalf("expected Claude-style thinking signature, got %q", resp.Content[0].Signature)
	}
	if !strings.HasPrefix(resp.ID, "msg_") || strings.Contains(resp.ID, "-") {
		t.Fatalf("expected Anthropic-style message id without hyphens, got %q", resp.ID)
	}
}

func TestSanitizeClaudeSignatureSensitiveHistoryConvertsThinkingBlocks(t *testing.T) {
	req := &ClaudeRequest{
		Model: "claude-opus-4.8",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "start"},
			{Role: "assistant", Content: []interface{}{
				map[string]interface{}{"type": "text", "text": "visible"},
				map[string]interface{}{"type": "thinking", "thinking": "private plan", "signature": "bad"},
				map[string]interface{}{"type": "redacted_thinking", "data": "opaque"},
				map[string]interface{}{"thinking": "legacy plan", "signature": "also-bad"},
				map[string]interface{}{"type": "tool_use", "id": "t1", "name": "read", "input": map[string]interface{}{"path": "a.txt"}},
			}},
			{Role: "user", Content: "continue"},
		},
	}

	sanitized := sanitizeClaudeSignatureSensitiveHistory(req)
	blocks, ok := sanitized.Messages[1].Content.([]interface{})
	if !ok {
		t.Fatalf("expected sanitized content blocks, got %#v", sanitized.Messages[1].Content)
	}

	var texts []string
	for _, raw := range blocks {
		block, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if block["type"] == "redacted_thinking" {
			t.Fatalf("redacted_thinking block should be removed: %#v", block)
		}
		if block["signature"] != nil {
			t.Fatalf("signature field should not survive sanitization: %#v", block)
		}
		if block["type"] == "text" {
			texts = append(texts, block["text"].(string))
		}
	}

	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "visible") || !strings.Contains(joined, "private plan") || !strings.Contains(joined, "legacy plan") {
		t.Fatalf("expected text and thinking content to survive as text, got %q", joined)
	}

	originalBlocks := req.Messages[1].Content.([]interface{})
	if originalBlocks[1].(map[string]interface{})["type"] != "thinking" {
		t.Fatalf("sanitization should not mutate the original request")
	}
}

func TestClaudeToKiroPreservesSanitizedThinkingHistory(t *testing.T) {
	req := &ClaudeRequest{
		Model: "claude-opus-4.8",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "start"},
			{Role: "assistant", Content: []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "private plan", "signature": "bad"},
				map[string]interface{}{"type": "redacted_thinking", "data": "opaque"},
			}},
			{Role: "user", Content: "continue"},
		},
	}

	payload := ClaudeToKiro(req, false)
	var historyText strings.Builder
	for _, h := range payload.ConversationState.History {
		if h.AssistantResponseMessage != nil {
			historyText.WriteString(h.AssistantResponseMessage.Content)
		}
	}

	got := historyText.String()
	if !strings.Contains(got, "private plan") {
		t.Fatalf("expected sanitized thinking to survive in assistant history, got %q", got)
	}
	if strings.Contains(got, "opaque") || strings.Contains(got, "bad") {
		t.Fatalf("redacted data and signatures must not survive in history, got %q", got)
	}
}

func TestToolResultsContinuationIncludesInstructionPrefix(t *testing.T) {
	req := &OpenAIRequest{
		Model: "claude-sonnet-4.5",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "find data"},
			{Role: "assistant", ToolCalls: []ToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "fetch", Arguments: "{}"},
			}}},
			{Role: "tool", ToolCallID: "call_1", Content: "result-1"},
		},
	}

	payload := OpenAIToKiro(req, false)
	content := payload.ConversationState.CurrentMessage.UserInputMessage.Content

	if !strings.Contains(content, toolResultsContinuationPrefix) {
		t.Fatalf("expected tool continuation prefix, got %q", content)
	}
	if !strings.Contains(content, "result-1") {
		t.Fatalf("expected tool result text in continuation content, got %q", content)
	}
}

func TestEnsureObjectSchemaRemovesKiroRejectedFieldsRecursively(t *testing.T) {
	input := map[string]interface{}{
		"type":                 "object",
		"required":             []interface{}{},
		"additionalProperties": false,
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":                 "string",
				"required":             nil,
				"additionalProperties": map[string]interface{}{"type": "string"},
			},
			"options": map[string]interface{}{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]interface{}{
					"force": map[string]interface{}{"type": "boolean"},
				},
			},
		},
		"anyOf": []interface{}{
			map[string]interface{}{
				"type":                 "object",
				"required":             []interface{}{},
				"additionalProperties": false,
			},
		},
	}

	got := ensureObjectSchema(input).(map[string]interface{})
	if schemaContainsKey(got, "additionalProperties") {
		t.Fatalf("expected additionalProperties to be removed recursively, got %#v", got)
	}
	if schemaContainsKey(got, "required") {
		t.Fatalf("expected empty/nil required fields to be removed recursively, got %#v", got)
	}
	if _, stillPresent := input["additionalProperties"]; !stillPresent {
		t.Fatalf("expected sanitizer not to mutate caller schema")
	}
}

func TestConvertOpenAIToolsSanitizesSchemaAndDescription(t *testing.T) {
	var tool OpenAITool
	tool.Type = "function"
	tool.Function.Name = "read_file"
	tool.Function.Parameters = map[string]interface{}{
		"type":                 "object",
		"required":             []string{},
		"additionalProperties": false,
	}

	tools := convertOpenAITools([]OpenAITool{tool})
	if len(tools) != 1 {
		t.Fatalf("expected one converted tool, got %d", len(tools))
	}
	if strings.TrimSpace(tools[0].ToolSpecification.Description) == "" {
		t.Fatalf("expected fallback tool description")
	}
	schema := tools[0].ToolSpecification.InputSchema.JSON.(map[string]interface{})
	if schemaContainsKey(schema, "additionalProperties") {
		t.Fatalf("expected OpenAI tool schema to be sanitized, got %#v", schema)
	}
	if schemaContainsKey(schema, "required") {
		t.Fatalf("expected empty required field to be removed, got %#v", schema)
	}
}

func TestConvertClaudeToolsMapsWebSearchServerToolToKiroWebSearch(t *testing.T) {
	tools, nameMap := convertClaudeTools([]ClaudeTool{{
		Type:    "web_search_20250305",
		Name:    "web_search",
		MaxUses: 2,
	}})

	if len(tools) != 1 {
		t.Fatalf("expected one converted tool, got %d", len(tools))
	}
	if got := tools[0].ToolSpecification.Name; got != "webSearch" {
		t.Fatalf("expected Kiro webSearch tool name, got %q", got)
	}
	if nameMap["webSearch"] != "web_search" {
		t.Fatalf("expected webSearch to map back to Claude web_search, got %#v", nameMap)
	}
	schema, ok := tools[0].ToolSpecification.InputSchema.JSON.(map[string]interface{})
	if !ok {
		t.Fatalf("expected object schema, got %#v", tools[0].ToolSpecification.InputSchema.JSON)
	}
	if schema["type"] != "object" {
		t.Fatalf("expected object schema, got %#v", schema)
	}
}

func TestKiroToClaudeResponseMapsWebSearchToServerToolUse(t *testing.T) {
	resp := KiroToClaudeResponseWithToolResults(
		"Search-backed answer.",
		"",
		false,
		[]KiroToolUse{{
			ToolUseID: "toolu_search1",
			Name:      "webSearch",
			Input:     map[string]interface{}{"query": "current date today"},
		}},
		[]KiroToolResult{{
			ToolUseID: "toolu_search1",
			RawContent: []interface{}{
				map[string]interface{}{
					"title":   "Today's Date",
					"url":     "https://example.com/date",
					"snippet": "Today is Monday, June 29, 2026.",
				},
			},
			Status: "success",
		}},
		10,
		20,
		"claude-opus-4-8",
	)

	if resp.StopReason != "end_turn" {
		t.Fatalf("web search server tool must not use client tool stop_reason, got %q", resp.StopReason)
	}
	if resp.Usage.ServerToolUse["web_search_requests"] != 1 {
		t.Fatalf("expected web search usage count, got %#v", resp.Usage.ServerToolUse)
	}
	var serverBlock, resultBlock *ClaudeContentBlock
	for i := range resp.Content {
		switch resp.Content[i].Type {
		case "server_tool_use":
			serverBlock = &resp.Content[i]
		case "web_search_tool_result":
			resultBlock = &resp.Content[i]
		}
	}
	if serverBlock == nil {
		t.Fatalf("expected server_tool_use block, got %#v", resp.Content)
	}
	if serverBlock.Name != "web_search" || !strings.HasPrefix(serverBlock.ID, "srvtoolu_") {
		t.Fatalf("unexpected server tool block: %#v", serverBlock)
	}
	if resultBlock == nil {
		t.Fatalf("expected web_search_tool_result block, got %#v", resp.Content)
	}
	results, ok := resultBlock.Content.([]interface{})
	if !ok || len(results) != 1 {
		t.Fatalf("expected one normalized search result, got %#v", resultBlock.Content)
	}
	first, ok := results[0].(map[string]interface{})
	if !ok || first["type"] != "web_search_result" || first["page_content"] == "" {
		t.Fatalf("expected normalized web_search_result, got %#v", results[0])
	}
}

func schemaContainsKey(value interface{}, key string) bool {
	switch v := value.(type) {
	case map[string]interface{}:
		if _, ok := v[key]; ok {
			return true
		}
		for _, child := range v {
			if schemaContainsKey(child, key) {
				return true
			}
		}
	case []interface{}:
		for _, child := range v {
			if schemaContainsKey(child, key) {
				return true
			}
		}
	}
	return false
}

func TestParseModelAndThinking(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantModel    string
		wantThinking bool
	}{
		// Format normalization: dash → dot for new versions without code changes.
		{"new opus dash form", "claude-opus-4-8", "claude-opus-4.8", false},
		{"new opus dot form", "claude-opus-4.8", "claude-opus-4.8", false},
		{"existing opus dash form", "claude-opus-4-7", "claude-opus-4.7", false},
		{"existing opus dot form", "claude-opus-4.7", "claude-opus-4.7", false},
		{"sonnet dash form", "claude-sonnet-4-6", "claude-sonnet-4.6", false},
		{"sonnet dot form", "claude-sonnet-4.6", "claude-sonnet-4.6", false},
		{"haiku dash form", "claude-haiku-4-5", "claude-haiku-4.5", false},
		{"haiku dot form", "claude-haiku-4.5", "claude-haiku-4.5", false},
		{"future major bump", "claude-sonnet-5-0", "claude-sonnet-5.0", false},

		// Bare family name passes through (no minor to normalize).
		{"bare sonnet 4", "claude-sonnet-4", "claude-sonnet-4", false},

		// Dated snapshot must hit the alias before the regex rewrites it.
		{"dated sonnet snapshot", "claude-sonnet-4-20250514", "claude-sonnet-4", false},

		// Cross-family legacy IDs.
		{"claude 3.5 sonnet", "claude-3-5-sonnet", "claude-sonnet-4.5", false},
		{"claude 3 opus", "claude-3-opus", "claude-sonnet-4.5", false},
		{"claude 3 sonnet", "claude-3-sonnet", "claude-sonnet-4", false},
		{"claude 3 haiku", "claude-3-haiku", "claude-haiku-4.5", false},

		// Non-Anthropic fallbacks.
		{"gpt-4-turbo", "gpt-4-turbo", "claude-sonnet-4.5", false},
		{"gpt-4o", "gpt-4o", "claude-sonnet-4.5", false},
		{"gpt-4", "gpt-4", "claude-sonnet-4.5", false},
		{"gpt-3.5-turbo", "gpt-3.5-turbo", "claude-sonnet-4.5", false},

		// Thinking suffix is stripped before mapping.
		{"thinking suffix on dash form", "claude-opus-4-8-thinking", "claude-opus-4.8", true},
		{"thinking suffix on dot form", "claude-sonnet-4.5-thinking", "claude-sonnet-4.5", true},
		{"thinking suffix on legacy alias", "claude-3-5-sonnet-thinking", "claude-sonnet-4.5", true},

		// Unknown models pass through unchanged.
		{"unknown model", "some-other-model", "some-other-model", false},
		{"misspelled claude family", "claude-opux-4-8", "claude-opux-4-8", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotModel, gotThinking := ParseModelAndThinking(tc.input, "-thinking")
			if gotModel != tc.wantModel {
				t.Errorf("model: got %q, want %q", gotModel, tc.wantModel)
			}
			if gotThinking != tc.wantThinking {
				t.Errorf("thinking: got %v, want %v", gotThinking, tc.wantThinking)
			}
		})
	}
}

func TestParseModelAndThinkingDoesNotRewriteDatedSnapshotMinor(t *testing.T) {
	// Guards the \b boundary in claudeVersionPattern: without it, the regex would
	// rewrite "claude-sonnet-4-20250514" to "claude-sonnet-4.20250514" before the
	// alias table could redirect it.
	got, _ := ParseModelAndThinking("claude-sonnet-4-20250514", "-thinking")
	if got != "claude-sonnet-4" {
		t.Fatalf("dated snapshot must alias to claude-sonnet-4, got %q", got)
	}
	if strings.Contains(got, ".") {
		t.Fatalf("dated snapshot must not be rewritten with a dot, got %q", got)
	}
}

func TestClaudeToolResultImageAttachedToCurrentMessage(t *testing.T) {
	const imgData = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	req := &ClaudeRequest{
		Model: "claude-opus-4.8",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "read this image"},
			{
				Role: "assistant",
				Content: []interface{}{
					map[string]interface{}{"type": "tool_use", "id": "tool_1", "name": "read", "input": map[string]interface{}{"path": "a.png"}},
				},
			},
			{
				Role: "user",
				Content: []interface{}{
					map[string]interface{}{
						"type":        "tool_result",
						"tool_use_id": "tool_1",
						"content": []interface{}{
							map[string]interface{}{
								"type": "image",
								"source": map[string]interface{}{
									"type":       "base64",
									"media_type": "image/png",
									"data":       imgData,
								},
							},
						},
					},
				},
			},
		},
	}

	payload := ClaudeToKiro(req, false)
	cur := payload.ConversationState.CurrentMessage.UserInputMessage
	if len(cur.Images) != 1 {
		t.Fatalf("expected tool_result image attached to current message, got %d images", len(cur.Images))
	}
	if cur.Images[0].Format != "png" || cur.Images[0].Source.Bytes != imgData {
		t.Fatalf("unexpected image payload: %+v", cur.Images[0])
	}
	if cur.UserInputMessageContext == nil || len(cur.UserInputMessageContext.ToolResults) != 1 {
		t.Fatalf("expected one tool result preserved")
	}
	if strings.TrimSpace(cur.UserInputMessageContext.ToolResults[0].Content[0].Text) == "" {
		t.Fatalf("expected non-empty placeholder text for image-only tool result")
	}
}

func TestClaudeToolResultMixedTextAndImage(t *testing.T) {
	const imgData = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	req := &ClaudeRequest{
		Model: "claude-opus-4.8",
		Messages: []ClaudeMessage{
			{
				Role: "user",
				Content: []interface{}{
					map[string]interface{}{
						"type":        "tool_result",
						"tool_use_id": "tool_2",
						"content": []interface{}{
							map[string]interface{}{"type": "text", "text": "here is the screenshot"},
							map[string]interface{}{
								"type": "image",
								"source": map[string]interface{}{
									"type":       "base64",
									"media_type": "image/png",
									"data":       imgData,
								},
							},
						},
					},
				},
			},
		},
	}

	payload := ClaudeToKiro(req, false)
	cur := payload.ConversationState.CurrentMessage.UserInputMessage
	if len(cur.Images) != 1 {
		t.Fatalf("expected one image extracted, got %d", len(cur.Images))
	}
	if cur.UserInputMessageContext != nil && len(cur.UserInputMessageContext.ToolResults) != 0 {
		t.Fatalf("orphaned tool result must be flattened instead of sent as structured data")
	}
	if !strings.Contains(cur.Content, "here is the screenshot") {
		t.Fatalf("expected original tool text preserved, got %q", cur.Content)
	}
}

func TestClaudeToKiroExtractsTextDocumentBlock(t *testing.T) {
	req := &ClaudeRequest{
		Model: "claude-opus-4.8",
		Messages: []ClaudeMessage{{
			Role: "user",
			Content: []interface{}{
				map[string]interface{}{
					"type":  "document",
					"title": "note.txt",
					"source": map[string]interface{}{
						"type":       "text",
						"media_type": "text/plain",
						"data":       "The project codename is ORCHID-913.",
					},
				},
				map[string]interface{}{"type": "text", "text": "What is the project codename?"},
			},
		}},
	}

	payload := ClaudeToKiro(req, false)
	content := payload.ConversationState.CurrentMessage.UserInputMessage.Content
	if !strings.Contains(content, "[Document: note.txt]") || !strings.Contains(content, "ORCHID-913") {
		t.Fatalf("expected document text to be included in prompt, got %q", content)
	}
	if !strings.Contains(content, "What is the project codename?") {
		t.Fatalf("expected user question to remain in prompt, got %q", content)
	}
}

func TestClaudeDocumentExtractionSupportsBase64PDFText(t *testing.T) {
	pdf := `%PDF-1.4
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>
endobj
4 0 obj
<< /Length 56 >>
stream
BT /F1 12 Tf 72 720 Td (Invoice total: ALPHA-42) Tj ET
endstream
endobj
%%EOF`
	block := map[string]interface{}{
		"type": "document",
		"name": "invoice.pdf",
		"source": map[string]interface{}{
			"type":       "base64",
			"media_type": "application/pdf",
			"data":       base64.StdEncoding.EncodeToString([]byte(pdf)),
		},
	}

	got := extractClaudeDocumentText(block)
	if !strings.Contains(got, "[Document: invoice.pdf]") || !strings.Contains(got, "ALPHA-42") {
		t.Fatalf("expected PDF text extraction, got %q", got)
	}
}

func TestOpenAIToolResultImageAttachedToCurrentMessage(t *testing.T) {
	const dataURL = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	req := &OpenAIRequest{
		Model: "claude-sonnet-4.5",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "look at the file"},
			{
				Role:       "tool",
				ToolCallID: "call_img",
				Content: []interface{}{
					map[string]interface{}{
						"type":      "image_url",
						"image_url": map[string]interface{}{"url": dataURL},
					},
				},
			},
		},
	}

	payload := OpenAIToKiro(req, false)
	cur := payload.ConversationState.CurrentMessage.UserInputMessage
	if len(cur.Images) != 1 {
		t.Fatalf("expected tool image attached to current message, got %d", len(cur.Images))
	}
	if cur.Images[0].Format != "png" {
		t.Fatalf("expected png format, got %q", cur.Images[0].Format)
	}
}

func TestOpenAIToolResultImageCarriedWhenFollowedByUser(t *testing.T) {
	const dataURL = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	req := &OpenAIRequest{
		Model: "claude-sonnet-4.5",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "look at the file"},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{
						ID:   "call_img",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{Name: "read", Arguments: `{"path":"a.png"}`},
					},
				},
			},
			{
				Role:       "tool",
				ToolCallID: "call_img",
				Content: []interface{}{
					map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": dataURL}},
				},
			},
			{Role: "user", Content: "what do you see?"},
		},
	}

	payload := OpenAIToKiro(req, false)

	var toolHistImages int
	for _, h := range payload.ConversationState.History {
		if h.UserInputMessage != nil && strings.Contains(h.UserInputMessage.Content, toolResultsContinuationPrefix) {
			toolHistImages += len(h.UserInputMessage.Images)
		}
	}
	if toolHistImages != 1 {
		t.Fatalf("expected tool image carried on the flushed tool-result history entry, got %d", toolHistImages)
	}

	cur := payload.ConversationState.CurrentMessage.UserInputMessage
	if len(cur.Images) != 0 {
		t.Fatalf("tool image should not leak into a later user message, got %d on current", len(cur.Images))
	}
}
