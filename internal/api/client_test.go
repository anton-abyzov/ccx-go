package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient(t *testing.T) {
	c := NewClient("test-key")
	assert.Equal(t, "test-key", c.apiKey)
	assert.Equal(t, defaultBaseURL, c.baseURL)
	assert.Equal(t, defaultVersion, c.version)
}

func TestClient_WithBaseURL(t *testing.T) {
	c := NewClient("key").WithBaseURL("http://localhost:8080")
	assert.Equal(t, "http://localhost:8080", c.baseURL)
}

func TestClient_CreateMessage(t *testing.T) {
	resp := Response{
		ID:   "msg_123",
		Type: "message",
		Role: RoleAssistant,
		Content: []ContentBlock{
			{Type: "text", Text: "Hello!"},
		},
		StopReason: "end_turn",
		Usage:      Usage{InputTokens: 10, OutputTokens: 5},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "test-key", r.Header.Get("X-API-Key"))
		assert.Equal(t, defaultVersion, r.Header.Get("Anthropic-Version"))
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/messages", r.URL.Path)

		var req Request
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.False(t, req.Stream)
		assert.Equal(t, "claude-sonnet-4-20250514", req.Model)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient("test-key").WithBaseURL(server.URL)
	got, err := client.CreateMessage(context.Background(), &Request{
		Model:    "claude-sonnet-4-20250514",
		Messages: []Message{{Role: RoleUser, Content: []ContentBlock{NewTextBlock("Hi")}}},
	})

	require.NoError(t, err)
	assert.Equal(t, "msg_123", got.ID)
	assert.Equal(t, "Hello!", got.Content[0].Text)
	assert.Equal(t, "end_turn", got.StopReason)
}

func TestClient_CreateMessage_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"type":    "authentication_error",
				"message": "invalid api key",
			},
		})
	}))
	defer server.Close()

	client := NewClient("bad-key").WithBaseURL(server.URL)
	_, err := client.CreateMessage(context.Background(), &Request{
		Model:    "claude-sonnet-4-20250514",
		Messages: []Message{{Role: RoleUser, Content: []ContentBlock{NewTextBlock("Hi")}}},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication_error")
	assert.Contains(t, err.Error(), "invalid api key")
}

func TestClient_RateLimitRetry(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"type": "rate_limit_error", "message": "too many requests"},
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Response{
			ID:         "msg_retry",
			Content:    []ContentBlock{{Type: "text", Text: "success after retry"}},
			StopReason: "end_turn",
		})
	}))
	defer server.Close()

	client := NewClient("key").WithBaseURL(server.URL)
	got, err := client.CreateMessage(context.Background(), &Request{
		Model:    "claude-sonnet-4-20250514",
		Messages: []Message{{Role: RoleUser, Content: []ContentBlock{NewTextBlock("Hi")}}},
	})

	require.NoError(t, err)
	assert.Equal(t, "success after retry", got.Content[0].Text)
	assert.GreaterOrEqual(t, int(attempts.Load()), 3)
}

func TestClient_ServerErrorRetry(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Response{
			ID:         "msg_ok",
			Content:    []ContentBlock{{Type: "text", Text: "ok"}},
			StopReason: "end_turn",
		})
	}))
	defer server.Close()

	client := NewClient("key").WithBaseURL(server.URL)
	got, err := client.CreateMessage(context.Background(), &Request{
		Model:    "claude-sonnet-4-20250514",
		Messages: []Message{{Role: RoleUser, Content: []ContentBlock{NewTextBlock("Hi")}}},
	})

	require.NoError(t, err)
	assert.Equal(t, "ok", got.Content[0].Text)
}

func TestClient_CreateMessageStream(t *testing.T) {
	sseData := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-20250514","usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		assert.True(t, req.Stream)

		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseData))
	}))
	defer server.Close()

	client := NewClient("test-key").WithBaseURL(server.URL)
	stream, err := client.CreateMessageStream(context.Background(), &Request{
		Model:    "claude-sonnet-4-20250514",
		Messages: []Message{{Role: RoleUser, Content: []ContentBlock{NewTextBlock("Hi")}}},
	})
	require.NoError(t, err)
	defer stream.Close()

	acc := NewAccumulator()
	for {
		event, err := stream.Next()
		if err != nil {
			break
		}
		if acc.Process(event) {
			break
		}
	}

	resp := acc.Response()
	require.NotNil(t, resp)
	assert.Equal(t, "msg_1", resp.ID)
	assert.Equal(t, "end_turn", resp.StopReason)
	require.Len(t, resp.Content, 1)
	assert.Equal(t, "Hello world", resp.Content[0].Text)
}

func TestClient_SetHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "my-key", r.Header.Get("X-API-Key"))
		assert.Equal(t, defaultVersion, r.Header.Get("Anthropic-Version"))
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(Response{})
	}))
	defer server.Close()

	client := NewClient("my-key").WithBaseURL(server.URL)
	client.CreateMessage(context.Background(), &Request{
		Model:    "claude-sonnet-4-20250514",
		Messages: []Message{{Role: RoleUser, Content: []ContentBlock{NewTextBlock("test")}}},
	})
}

func TestClient_SetHeaders_OAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer oauth-token", r.Header.Get("Authorization"))
		assert.Equal(t, "oauth-2025-04-20", r.Header.Get("anthropic-beta"))
		assert.Empty(t, r.Header.Get("X-API-Key"))
		assert.Equal(t, defaultVersion, r.Header.Get("Anthropic-Version"))
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(Response{})
	}))
	defer server.Close()

	client := NewClient("oauth-token").WithBaseURL(server.URL).WithOAuth(true)
	client.CreateMessage(context.Background(), &Request{
		Model:    "claude-sonnet-4-20250514",
		Messages: []Message{{Role: RoleUser, Content: []ContentBlock{NewTextBlock("test")}}},
	})
}

func TestClient_WithVertex(t *testing.T) {
	c := NewClient("gcp-token").WithVertex("us-east5", "my-project")
	assert.NotNil(t, c.vertex)
	assert.Equal(t, "us-east5", c.vertex.Region)
	assert.Equal(t, "my-project", c.vertex.ProjectID)
}

func TestClient_BuildURL_Standard(t *testing.T) {
	c := NewClient("key")
	assert.Equal(t, "https://api.anthropic.com/v1/messages", c.buildURL("claude-sonnet-4-20250514", false))
	assert.Equal(t, "https://api.anthropic.com/v1/messages", c.buildURL("claude-sonnet-4-20250514", true))
}

func TestClient_BuildURL_Vertex(t *testing.T) {
	c := NewClient("token").WithVertex("us-east5", "my-project")

	got := c.buildURL("claude-sonnet-4-20250514", false)
	assert.Equal(t, "https://us-east5-aiplatform.googleapis.com/v1/projects/my-project/locations/us-east5/publishers/anthropic/models/claude-sonnet-4@20250514:rawPredict", got)

	got = c.buildURL("claude-sonnet-4-20250514", true)
	assert.Equal(t, "https://us-east5-aiplatform.googleapis.com/v1/projects/my-project/locations/us-east5/publishers/anthropic/models/claude-sonnet-4@20250514:streamRawPredict", got)
}

func TestToVertexModelID(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"claude-sonnet-4-20250514", "claude-sonnet-4@20250514"},
		{"claude-opus-4-20250514", "claude-opus-4@20250514"},
		{"claude-haiku-4-5-20251001", "claude-haiku-4-5@20251001"},
		{"claude-3-5-sonnet-20241022", "claude-3-5-sonnet@20241022"},
		// Already Vertex format
		{"claude-sonnet-4@20250514", "claude-sonnet-4@20250514"},
		// Alias without date
		{"claude-sonnet-4", "claude-sonnet-4"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, ToVertexModelID(tt.input))
		})
	}
}

func TestClient_MarshalRequest_Standard(t *testing.T) {
	c := NewClient("key")
	req := &Request{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 1024,
		Messages:  []Message{{Role: RoleUser, Content: []ContentBlock{NewTextBlock("Hi")}}},
		Stream:    true,
	}
	body, err := c.marshalRequest(req)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))
	assert.Equal(t, "claude-sonnet-4-20250514", parsed["model"])
	assert.NotContains(t, parsed, "anthropic_version")
}

func TestClient_MarshalRequest_Vertex(t *testing.T) {
	c := NewClient("token").WithVertex("us-east5", "my-project")
	req := &Request{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 1024,
		Messages:  []Message{{Role: RoleUser, Content: []ContentBlock{NewTextBlock("Hi")}}},
		System:    "Be helpful",
		Stream:    true,
	}
	body, err := c.marshalRequest(req)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))
	// Vertex format: has anthropic_version (vertex-specific), no model
	assert.Equal(t, vertexVersion, parsed["anthropic_version"])
	assert.NotContains(t, parsed, "model")
	assert.Equal(t, true, parsed["stream"])
	assert.Equal(t, "Be helpful", parsed["system"])
	assert.Equal(t, float64(1024), parsed["max_tokens"])
}

func TestClient_SetHeaders_Vertex(t *testing.T) {
	c := NewClient("gcp-token").WithVertex("us-east5", "my-project")
	req, _ := http.NewRequest("POST", "http://example.com", nil)
	c.setHeaders(req)

	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.Equal(t, "Bearer gcp-token", req.Header.Get("Authorization"))
	// Vertex should NOT send Anthropic-Version header or X-API-Key
	assert.Empty(t, req.Header.Get("Anthropic-Version"))
	assert.Empty(t, req.Header.Get("X-API-Key"))
	assert.Empty(t, req.Header.Get("anthropic-beta"))
}

func TestAPIError(t *testing.T) {
	err := &APIError{StatusCode: 429, Type: "rate_limit_error", Message: "too many requests"}
	assert.Contains(t, err.Error(), "429")
	assert.Contains(t, err.Error(), "rate_limit_error")
	assert.True(t, err.IsRateLimit())
	assert.False(t, err.IsOverloaded())

	err2 := &APIError{StatusCode: 529, Message: "overloaded"}
	assert.True(t, err2.IsOverloaded())
}

func TestRetryDelay(t *testing.T) {
	d1 := retryDelay(1, nil)
	assert.Equal(t, 1*time.Second, d1)

	d2 := retryDelay(2, nil)
	assert.Equal(t, 2*time.Second, d2)

	d3 := retryDelay(3, nil)
	assert.Equal(t, 4*time.Second, d3)

	// Should cap at 30s
	d10 := retryDelay(10, nil)
	assert.LessOrEqual(t, d10, 30*time.Second)
}

func TestIsRetryable(t *testing.T) {
	assert.True(t, isRetryable(429))
	assert.True(t, isRetryable(500))
	assert.True(t, isRetryable(529))
	assert.True(t, isRetryable(503))
	assert.False(t, isRetryable(401))
	assert.False(t, isRetryable(400))
	assert.False(t, isRetryable(200))
}

func TestParseAPIError(t *testing.T) {
	body := []byte(`{"error":{"type":"invalid_request","message":"bad input"}}`)
	err := parseAPIError(400, body)
	assert.Equal(t, 400, err.StatusCode)
	assert.Equal(t, "invalid_request", err.Type)
	assert.Equal(t, "bad input", err.Message)

	// Non-JSON body
	err2 := parseAPIError(500, []byte("plain error"))
	assert.Equal(t, "plain error", err2.Message)

	// Empty body
	err3 := parseAPIError(500, []byte(""))
	assert.Equal(t, "Internal Server Error", err3.Message)
}
