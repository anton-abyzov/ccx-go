package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/anton-abyzov/ccx-go/internal/api"
	"github.com/anton-abyzov/ccx-go/internal/tool"
)

type input struct {
	Prompt      string `json:"prompt"`
	Description string `json:"description,omitempty"`
}

// RunFunc executes a sub-agent query loop and returns the text output.
type RunFunc func(ctx context.Context, prompt, model, system string, registry *tool.Registry) (string, error)

// Tool spawns a sub-agent with its own query loop.
type Tool struct {
	client   api.MessageClient
	registry *tool.Registry
	model    string
	system   string
}

// New creates a new Agent tool. The client, registry, model, and system prompt
// are shared with the parent so the sub-agent can call tools and talk to Claude.
func New(client api.MessageClient, registry *tool.Registry, model, system string) *Tool {
	return &Tool{
		client:   client,
		registry: registry,
		model:    model,
		system:   system,
	}
}

func (t *Tool) Name() string { return "Agent" }
func (t *Tool) Description() string {
	return "Launch a sub-agent to handle a complex task. The agent runs its own conversation loop with access to tools and returns the result."
}

func (t *Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"prompt": {"type": "string", "description": "The task for the agent to perform"},
			"description": {"type": "string", "description": "A short (3-5 word) description of the task"}
		},
		"required": ["prompt"]
	}`)
}

func (t *Tool) Execute(ctx context.Context, rawInput json.RawMessage) (*tool.Result, error) {
	var in input
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return &tool.Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}

	if in.Prompt == "" {
		return &tool.Result{Content: "prompt is required", IsError: true}, nil
	}

	desc := in.Description
	if desc == "" {
		desc = "sub-agent"
	}
	fmt.Fprintf(logWriter, "\n[agent: %s] starting...\n", desc)

	output, err := t.runSubAgent(ctx, in.Prompt)
	if err != nil {
		return &tool.Result{
			Content: fmt.Sprintf("agent error: %v", err),
			IsError: true,
		}, nil
	}

	fmt.Fprintf(logWriter, "[agent: %s] done\n", desc)
	return &tool.Result{Content: output}, nil
}

// runSubAgent creates a minimal query loop for the sub-agent.
func (t *Tool) runSubAgent(ctx context.Context, prompt string) (string, error) {
	messages := []api.Message{
		{
			Role:    api.RoleUser,
			Content: []api.ContentBlock{api.NewTextBlock(prompt)},
		},
	}

	const maxTurns = 100
	var collectedText strings.Builder

	for turn := 0; turn < maxTurns; turn++ {
		if ctx.Err() != nil {
			return collectedText.String(), ctx.Err()
		}

		req := &api.Request{
			Model:     t.model,
			MaxTokens: 8192,
			Messages:  messages,
			System:    t.system,
			Tools:     t.registry.APIDefinitions(),
		}

		stream, err := t.client.CreateMessageStream(ctx, req)
		if err != nil {
			return collectedText.String(), fmt.Errorf("API request failed: %w", err)
		}

		acc := api.NewAccumulator()
		for {
			event, err := stream.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				stream.Close()
				return collectedText.String(), fmt.Errorf("reading stream: %w", err)
			}
			if acc.Process(event) {
				break
			}
		}
		stream.Close()

		resp := acc.Response()
		if resp == nil {
			return collectedText.String(), fmt.Errorf("no response received")
		}

		// Collect text output
		for _, block := range resp.Content {
			if block.Type == "text" && block.Text != "" {
				collectedText.WriteString(block.Text)
			}
		}

		messages = append(messages, api.Message{
			Role:    api.RoleAssistant,
			Content: resp.Content,
		})

		// Extract tool uses
		var toolUses []api.ContentBlock
		for _, b := range resp.Content {
			if b.Type == "tool_use" {
				toolUses = append(toolUses, b)
			}
		}

		if len(toolUses) == 0 {
			break
		}

		// Execute tools
		var results []api.ContentBlock
		for _, tu := range toolUses {
			t := t.registry.FindByName(tu.Name)
			if t == nil {
				results = append(results, api.NewToolResultBlock(tu.ID, fmt.Sprintf("tool %q not found", tu.Name), true))
				continue
			}
			result, err := t.Execute(ctx, tu.Input)
			if err != nil {
				results = append(results, api.NewToolResultBlock(tu.ID, fmt.Sprintf("tool error: %v", err), true))
				continue
			}
			results = append(results, api.NewToolResultBlock(tu.ID, result.Content, result.IsError))
		}

		messages = append(messages, api.Message{
			Role:    api.RoleUser,
			Content: results,
		})
	}

	if collectedText.Len() == 0 {
		return "(agent produced no text output)", nil
	}
	return collectedText.String(), nil
}

// logWriter defaults to stderr for agent status messages.
var logWriter io.Writer = struct{ io.Writer }{writerFunc(func(p []byte) (int, error) {
	return fmt.Fprint(io.Discard, string(p))
})}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func init() {
	// Default: write agent status to stderr
	logWriter = stderrWriter{}
}

type stderrWriter struct{}

func (stderrWriter) Write(p []byte) (int, error) {
	return fmt.Fprint(io.Discard, "") // suppress by default; main can override
}

// SetLogWriter sets where agent status messages go (e.g., os.Stderr).
func SetLogWriter(w io.Writer) {
	logWriter = w
}
