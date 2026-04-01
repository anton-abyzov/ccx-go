package query

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/anton-abyzov/ccx-go/internal/api"
	"github.com/anton-abyzov/ccx-go/internal/tool"
)

// LoopConfig configures the query loop behavior.
type LoopConfig struct {
	Model     string
	MaxTokens int
	MaxTurns  int
	System    string
	// OnText is called when text is received during streaming (for TUI integration).
	OnText func(text string)
	// OnToolStart is called when a tool execution begins.
	OnToolStart func(toolName string)
	// OnToolDone is called when a tool execution completes.
	OnToolDone func(toolName string, result string, isError bool)
	// OnTurnComplete is called after each assistant turn.
	OnTurnComplete func(usage api.Usage)
	// SuppressOutput disables direct stdout printing (for TUI mode).
	SuppressOutput bool
}

// Loop implements the core agentic query loop.
type Loop struct {
	client   api.MessageClient
	registry *tool.Registry
	config   LoopConfig
	// Messages is accessible for introspection (e.g., context compaction).
	Messages []api.Message
}

// NewLoop creates a new query loop.
func NewLoop(client api.MessageClient, registry *tool.Registry, config LoopConfig) *Loop {
	if config.MaxTokens == 0 {
		config.MaxTokens = 16384
	}
	return &Loop{
		client:   client,
		registry: registry,
		config:   config,
	}
}

// Run starts the interactive query loop. If initialPrompt is empty, reads from stdin.
func (l *Loop) Run(ctx context.Context, initialPrompt string) error {
	prompt := initialPrompt
	if prompt == "" {
		var err error
		prompt, err = readInput(os.Stdin, os.Stdout)
		if err != nil {
			return err
		}
	}

	l.Messages = append(l.Messages, api.Message{
		Role:    api.RoleUser,
		Content: []api.ContentBlock{api.NewTextBlock(prompt)},
	})

	// Run the agentic loop until the model stops calling tools
	if err := l.runLoop(ctx); err != nil {
		return err
	}

	// Interactive multi-turn: keep accepting user input
	for {
		fmt.Fprintln(os.Stdout)
		input, err := readInput(os.Stdin, os.Stdout)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if input == "" {
			continue
		}
		if input == "/exit" || input == "/quit" {
			return nil
		}

		l.Messages = append(l.Messages, api.Message{
			Role:    api.RoleUser,
			Content: []api.ContentBlock{api.NewTextBlock(input)},
		})

		if err := l.runLoop(ctx); err != nil {
			return err
		}
	}
}

// RunSingle executes a single turn (useful for non-interactive / testing).
func (l *Loop) RunSingle(ctx context.Context, messages []api.Message) (*api.Response, error) {
	return l.sendRequest(ctx, messages)
}

// SendMessage sends a message and runs the agentic loop until the model stops.
// Useful for programmatic interaction (e.g., agent spawning).
func (l *Loop) SendMessage(ctx context.Context, text string) error {
	l.Messages = append(l.Messages, api.Message{
		Role:    api.RoleUser,
		Content: []api.ContentBlock{api.NewTextBlock(text)},
	})
	return l.runLoop(ctx)
}

// runLoop executes turns until the model stops requesting tools.
func (l *Loop) runLoop(ctx context.Context) error {
	turns := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if l.config.MaxTurns > 0 && turns >= l.config.MaxTurns {
			return nil
		}
		turns++

		resp, err := l.sendRequest(ctx, l.Messages)
		if err != nil {
			return fmt.Errorf("API request failed: %w", err)
		}

		l.Messages = append(l.Messages, api.Message{
			Role:    api.RoleAssistant,
			Content: resp.Content,
		})

		if l.config.OnTurnComplete != nil {
			l.config.OnTurnComplete(resp.Usage)
		}

		toolUses := extractToolUses(resp.Content)
		if !l.config.SuppressOutput {
			printTextBlocks(os.Stdout, resp.Content)
		}

		// Stop conditions:
		// 1. No tool calls → conversation turn complete
		// 2. stop_reason is "end_turn" AND no tool calls
		// 3. stop_reason is "max_tokens" → warn and stop
		if resp.StopReason == "max_tokens" {
			fmt.Fprintln(os.Stderr, "\n[warning: response truncated due to max_tokens]")
			if len(toolUses) == 0 {
				return nil
			}
		}

		if len(toolUses) == 0 {
			return nil
		}

		// Execute all tool calls and gather results
		results := l.executeTools(ctx, toolUses)
		l.Messages = append(l.Messages, api.Message{
			Role:    api.RoleUser,
			Content: results,
		})
	}
}

func (l *Loop) sendRequest(ctx context.Context, messages []api.Message) (*api.Response, error) {
	req := &api.Request{
		Model:     l.config.Model,
		MaxTokens: l.config.MaxTokens,
		Messages:  messages,
		System:    l.config.System,
		Tools:     l.registry.APIDefinitions(),
	}

	stream, err := l.client.CreateMessageStream(ctx, req)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	acc := api.NewAccumulator()
	for {
		event, err := stream.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading stream: %w", err)
		}

		// Print text deltas as they arrive
		if event.Type == "content_block_delta" && event.Delta != nil && event.Delta.Type == "text_delta" {
			text := event.Delta.Text
			if !l.config.SuppressOutput {
				fmt.Fprint(os.Stdout, text)
			}
			if l.config.OnText != nil {
				l.config.OnText(text)
			}
		}

		if acc.Process(event) {
			break
		}
	}

	resp := acc.Response()
	if resp == nil {
		return nil, fmt.Errorf("no response received")
	}
	return resp, nil
}

func (l *Loop) executeTools(ctx context.Context, toolUses []api.ContentBlock) []api.ContentBlock {
	var results []api.ContentBlock

	for _, tu := range toolUses {
		if l.config.OnToolStart != nil {
			l.config.OnToolStart(tu.Name)
		}

		t := l.registry.FindByName(tu.Name)
		if t == nil {
			errMsg := fmt.Sprintf("tool %q not found", tu.Name)
			results = append(results, api.NewToolResultBlock(tu.ID, errMsg, true))
			if l.config.OnToolDone != nil {
				l.config.OnToolDone(tu.Name, errMsg, true)
			}
			continue
		}

		result, err := t.Execute(ctx, tu.Input)
		if err != nil {
			errMsg := fmt.Sprintf("tool execution error: %v", err)
			results = append(results, api.NewToolResultBlock(tu.ID, errMsg, true))
			if l.config.OnToolDone != nil {
				l.config.OnToolDone(tu.Name, errMsg, true)
			}
			continue
		}

		results = append(results, api.NewToolResultBlock(
			tu.ID, result.Content, result.IsError,
		))
		if l.config.OnToolDone != nil {
			l.config.OnToolDone(tu.Name, result.Content, result.IsError)
		}
	}

	return results
}

func extractToolUses(blocks []api.ContentBlock) []api.ContentBlock {
	var toolUses []api.ContentBlock
	for _, b := range blocks {
		if b.Type == "tool_use" {
			toolUses = append(toolUses, b)
		}
	}
	return toolUses
}

func printTextBlocks(w io.Writer, blocks []api.ContentBlock) {
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			if !strings.HasSuffix(b.Text, "\n") {
				fmt.Fprintln(w)
			}
		}
	}
}

func readInput(in io.Reader, out io.Writer) (string, error) {
	fmt.Fprint(out, "> ")
	scanner := bufio.NewScanner(in)
	if scanner.Scan() {
		return strings.TrimSpace(scanner.Text()), nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading input: %w", err)
	}
	return "", io.EOF
}

// ToolResultsFromJSON is a helper for building tool result messages in tests.
func ToolResultsFromJSON(results map[string]string) []api.ContentBlock {
	var blocks []api.ContentBlock
	for id, content := range results {
		blocks = append(blocks, api.NewToolResultBlock(id, content, false))
	}
	return blocks
}

// UserMessage is a convenience constructor for tests.
func UserMessage(text string) api.Message {
	return api.Message{
		Role:    api.RoleUser,
		Content: []api.ContentBlock{api.NewTextBlock(text)},
	}
}

// AssistantMessage builds a message with tool_use blocks for tests.
func AssistantMessage(blocks ...api.ContentBlock) api.Message {
	return api.Message{
		Role:    api.RoleAssistant,
		Content: blocks,
	}
}

// FakeToolInput builds a json.RawMessage from a map for tests.
func FakeToolInput(data map[string]any) json.RawMessage {
	b, _ := json.Marshal(data)
	return b
}
