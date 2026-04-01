package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultOpenAIBaseURL = "https://openrouter.ai/api/v1"

// OpenAIClient is an HTTP client for OpenAI-compatible APIs (e.g., OpenRouter).
type OpenAIClient struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// NewOpenAIClient creates a client for OpenAI-compatible endpoints.
func NewOpenAIClient(apiKey string) *OpenAIClient {
	return &OpenAIClient{
		apiKey:     apiKey,
		baseURL:    defaultOpenAIBaseURL,
		httpClient: &http.Client{Timeout: 300 * time.Second},
	}
}

// WithBaseURL sets a custom base URL.
func (c *OpenAIClient) WithBaseURL(url string) *OpenAIClient {
	c.baseURL = url
	return c
}

// CreateMessage sends a non-streaming request via OpenAI-compatible API.
func (c *OpenAIClient) CreateMessage(ctx context.Context, req *Request) (*Response, error) {
	oaiReq := convertToOpenAI(req)
	oaiReq.Stream = false

	body, err := json.Marshal(oaiReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	c.setHeaders(httpReq)

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, &APIError{StatusCode: httpResp.StatusCode, Message: string(respBody)}
	}

	var oaiResp oaiResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return convertFromOpenAI(&oaiResp), nil
}

// CreateMessageStream sends a streaming request and returns a Streamer.
func (c *OpenAIClient) CreateMessageStream(ctx context.Context, req *Request) (Streamer, error) {
	oaiReq := convertToOpenAI(req)
	oaiReq.Stream = true

	body, err := json.Marshal(oaiReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	c.setHeaders(httpReq)

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		return nil, &APIError{StatusCode: httpResp.StatusCode, Message: string(respBody)}
	}

	return newOpenAIStreamAdapter(httpResp.Body, req.Model), nil
}

func (c *OpenAIClient) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
}

// --- OpenAI wire types ---

type oaiRequest struct {
	Model     string       `json:"model"`
	Messages  []oaiMessage `json:"messages"`
	MaxTokens int          `json:"max_tokens,omitempty"`
	Tools     []oaiTool    `json:"tools,omitempty"`
	Stream    bool         `json:"stream"`
}

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content,omitempty"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type oaiToolCall struct {
	ID       string      `json:"id"`
	Type     string      `json:"type"`
	Function oaiFunction `json:"function"`
}

type oaiFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaiTool struct {
	Type     string     `json:"type"`
	Function oaiToolDef `json:"function"`
}

type oaiToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type oaiResponse struct {
	ID      string      `json:"id"`
	Model   string      `json:"model"`
	Choices []oaiChoice `json:"choices"`
	Usage   *oaiUsage   `json:"usage,omitempty"`
}

type oaiChoice struct {
	Message      oaiMessage `json:"message"`
	FinishReason string     `json:"finish_reason"`
}

type oaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type oaiStreamChunk struct {
	ID      string             `json:"id"`
	Model   string             `json:"model"`
	Choices []oaiStreamChoice  `json:"choices"`
	Usage   *oaiUsage          `json:"usage,omitempty"`
}

type oaiStreamChoice struct {
	Delta        oaiStreamDelta `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

type oaiStreamDelta struct {
	Role      string               `json:"role,omitempty"`
	Content   string               `json:"content,omitempty"`
	ToolCalls []oaiStreamToolCall  `json:"tool_calls,omitempty"`
}

type oaiStreamToolCall struct {
	Index    int         `json:"index"`
	ID       string      `json:"id,omitempty"`
	Type     string      `json:"type,omitempty"`
	Function oaiFunction `json:"function"`
}

// --- Request conversion: Anthropic → OpenAI ---

func convertToOpenAI(req *Request) *oaiRequest {
	oai := &oaiRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
	}

	if req.System != "" {
		oai.Messages = append(oai.Messages, oaiMessage{
			Role:    "system",
			Content: req.System,
		})
	}

	for _, msg := range req.Messages {
		oai.Messages = append(oai.Messages, convertMessageToOpenAI(msg)...)
	}

	for _, t := range req.Tools {
		oai.Tools = append(oai.Tools, oaiTool{
			Type: "function",
			Function: oaiToolDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	return oai
}

func convertMessageToOpenAI(msg Message) []oaiMessage {
	var result []oaiMessage

	switch msg.Role {
	case RoleUser:
		var textParts []string
		var toolResults []oaiMessage

		for _, block := range msg.Content {
			switch block.Type {
			case "text":
				textParts = append(textParts, block.Text)
			case "tool_result":
				content := block.Content
				if block.IsError {
					content = "ERROR: " + content
				}
				toolResults = append(toolResults, oaiMessage{
					Role:       "tool",
					Content:    content,
					ToolCallID: block.ToolUseID,
				})
			}
		}

		if len(textParts) > 0 {
			result = append(result, oaiMessage{
				Role:    "user",
				Content: strings.Join(textParts, "\n"),
			})
		}
		result = append(result, toolResults...)

	case RoleAssistant:
		oaiMsg := oaiMessage{Role: "assistant"}
		var textParts []string

		for _, block := range msg.Content {
			switch block.Type {
			case "text":
				textParts = append(textParts, block.Text)
			case "tool_use":
				args := string(block.Input)
				if args == "" {
					args = "{}"
				}
				oaiMsg.ToolCalls = append(oaiMsg.ToolCalls, oaiToolCall{
					ID:   block.ID,
					Type: "function",
					Function: oaiFunction{
						Name:      block.Name,
						Arguments: args,
					},
				})
			}
		}

		oaiMsg.Content = strings.Join(textParts, "\n")
		result = append(result, oaiMsg)
	}

	return result
}

// --- Response conversion: OpenAI → Anthropic ---

func convertFromOpenAI(oai *oaiResponse) *Response {
	resp := &Response{
		ID:    oai.ID,
		Type:  "message",
		Role:  RoleAssistant,
		Model: oai.Model,
	}

	if oai.Usage != nil {
		resp.Usage = Usage{
			InputTokens:  oai.Usage.PromptTokens,
			OutputTokens: oai.Usage.CompletionTokens,
		}
	}

	if len(oai.Choices) > 0 {
		choice := oai.Choices[0]

		switch choice.FinishReason {
		case "stop":
			resp.StopReason = "end_turn"
		case "tool_calls":
			resp.StopReason = "tool_use"
		case "length":
			resp.StopReason = "max_tokens"
		default:
			resp.StopReason = "end_turn"
		}

		if choice.Message.Content != "" {
			resp.Content = append(resp.Content, NewTextBlock(choice.Message.Content))
		}

		for _, tc := range choice.Message.ToolCalls {
			resp.Content = append(resp.Content, NewToolUseBlock(
				tc.ID, tc.Function.Name, json.RawMessage(tc.Function.Arguments),
			))
		}
	}

	return resp
}

// --- Stream adapter: translates OpenAI SSE → Anthropic StreamEvents ---

type openAIStreamAdapter struct {
	reader      io.ReadCloser
	scanner     *bufio.Scanner
	model       string
	pending     []*StreamEvent
	started     bool
	textStarted bool
	blockIdx    int
	activeTools map[int]int // openai tool index → our block index
}

func newOpenAIStreamAdapter(r io.ReadCloser, model string) *openAIStreamAdapter {
	return &openAIStreamAdapter{
		reader:      r,
		scanner:     bufio.NewScanner(r),
		model:       model,
		activeTools: make(map[int]int),
	}
}

func (a *openAIStreamAdapter) Next() (*StreamEvent, error) {
	if len(a.pending) > 0 {
		ev := a.pending[0]
		a.pending = a.pending[1:]
		return ev, nil
	}

	for a.scanner.Scan() {
		line := a.scanner.Text()

		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			// If we haven't emitted message_stop yet, do so
			if a.started {
				if a.textStarted {
					a.pending = append(a.pending, &StreamEvent{Type: "content_block_stop", Index: a.blockIdx})
				}
				a.pending = append(a.pending, &StreamEvent{
					Type:  "message_delta",
					Delta: &Delta{StopReason: "end_turn"},
				})
				a.pending = append(a.pending, &StreamEvent{Type: "message_stop"})
			}
			if len(a.pending) > 0 {
				ev := a.pending[0]
				a.pending = a.pending[1:]
				return ev, nil
			}
			return nil, io.EOF
		}

		var chunk oaiStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		a.processChunk(&chunk)

		if len(a.pending) > 0 {
			ev := a.pending[0]
			a.pending = a.pending[1:]
			return ev, nil
		}
	}

	if err := a.scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading stream: %w", err)
	}

	return nil, io.EOF
}

func (a *openAIStreamAdapter) processChunk(chunk *oaiStreamChunk) {
	if !a.started {
		a.started = true
		a.pending = append(a.pending, &StreamEvent{
			Type: "message_start",
			Message: &Response{
				ID:    chunk.ID,
				Type:  "message",
				Role:  RoleAssistant,
				Model: chunk.Model,
				Usage: Usage{},
			},
		})
	}

	if len(chunk.Choices) == 0 {
		if chunk.Usage != nil {
			a.pending = append(a.pending, &StreamEvent{
				Type:  "message_delta",
				Usage: &Usage{OutputTokens: chunk.Usage.CompletionTokens},
			})
		}
		return
	}

	choice := chunk.Choices[0]
	delta := choice.Delta

	// Text content
	if delta.Content != "" {
		if !a.textStarted {
			a.textStarted = true
			a.pending = append(a.pending, &StreamEvent{
				Type:         "content_block_start",
				Index:        a.blockIdx,
				ContentBlock: &ContentBlock{Type: "text"},
			})
		}
		a.pending = append(a.pending, &StreamEvent{
			Type:  "content_block_delta",
			Index: a.blockIdx,
			Delta: &Delta{Type: "text_delta", Text: delta.Content},
		})
	}

	// Tool calls
	for _, tc := range delta.ToolCalls {
		if _, exists := a.activeTools[tc.Index]; !exists {
			// Close text block if open
			if a.textStarted {
				a.pending = append(a.pending, &StreamEvent{
					Type: "content_block_stop", Index: a.blockIdx,
				})
				a.blockIdx++
				a.textStarted = false
			}
			a.activeTools[tc.Index] = a.blockIdx
			a.pending = append(a.pending, &StreamEvent{
				Type:  "content_block_start",
				Index: a.blockIdx,
				ContentBlock: &ContentBlock{
					Type: "tool_use",
					ID:   tc.ID,
					Name: tc.Function.Name,
				},
			})
		}

		if tc.Function.Arguments != "" {
			idx := a.activeTools[tc.Index]
			a.pending = append(a.pending, &StreamEvent{
				Type:  "content_block_delta",
				Index: idx,
				Delta: &Delta{
					Type:        "input_json_delta",
					PartialJSON: tc.Function.Arguments,
				},
			})
		}
	}

	// Finish reason
	if choice.FinishReason != nil && *choice.FinishReason != "" {
		if a.textStarted {
			a.pending = append(a.pending, &StreamEvent{
				Type: "content_block_stop", Index: a.blockIdx,
			})
			a.textStarted = false
		}
		for _, idx := range a.activeTools {
			a.pending = append(a.pending, &StreamEvent{
				Type: "content_block_stop", Index: idx,
			})
		}

		stopReason := "end_turn"
		switch *choice.FinishReason {
		case "tool_calls":
			stopReason = "tool_use"
		case "length":
			stopReason = "max_tokens"
		}

		a.pending = append(a.pending, &StreamEvent{
			Type:  "message_delta",
			Delta: &Delta{StopReason: stopReason},
		})
		a.pending = append(a.pending, &StreamEvent{Type: "message_stop"})
	}
}

func (a *openAIStreamAdapter) Close() error {
	return a.reader.Close()
}
