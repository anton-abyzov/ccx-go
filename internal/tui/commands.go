package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/anton-abyzov/ccx-go/internal/config"
	"github.com/anton-abyzov/ccx-go/internal/cost"
	"github.com/anton-abyzov/ccx-go/internal/tool"
)

// CommandContext provides data needed by slash commands.
type CommandContext struct {
	Version  string
	Model    string
	Tracker  *cost.Tracker
	Registry *tool.Registry
}

// CommandResult holds the outcome of a slash command.
type CommandResult struct {
	Output  string
	Quit    bool
	Clear   bool
	Compact bool
}

// HandleSlashCommand processes /commands. Returns nil if input is not a command.
func HandleSlashCommand(input string, ctx CommandContext) *CommandResult {
	parts := strings.Fields(input)
	if len(parts) == 0 || !strings.HasPrefix(parts[0], "/") {
		return nil
	}

	switch parts[0] {
	case "/":
		return &CommandResult{Output: commandList()}
	case "/help":
		return &CommandResult{Output: helpText()}
	case "/exit", "/quit":
		return &CommandResult{Quit: true}
	case "/clear":
		return &CommandResult{Clear: true, Output: "\033[2J\033[H"}
	case "/cost":
		return &CommandResult{Output: costOutput(ctx.Tracker)}
	case "/model":
		return &CommandResult{Output: fmt.Sprintf("Model: %s (%s)", shortModelName(ctx.Model), ctx.Model)}
	case "/compact":
		return &CommandResult{Compact: true, Output: "Context compacted."}
	case "/version":
		return &CommandResult{Output: fmt.Sprintf("ccx-go v%s", ctx.Version)}
	case "/tools":
		return &CommandResult{Output: toolsOutput(ctx.Registry)}
	case "/login":
		if err := config.RunOAuthLogin(); err != nil {
			return &CommandResult{Output: fmt.Sprintf("Login failed: %v", err)}
		}
		return &CommandResult{Output: ""}
	default:
		return nil // not a built-in command; may be a skill
	}
}

func commandList() string {
	return `Available commands:
  /help     Show available commands and shortcuts
  /login    Log in to Claude via OAuth
  /exit     Quit the session
  /clear    Clear the screen
  /cost     Show token usage and cost for this session
  /model    Show current model
  /compact  Compress conversation context
  /version  Show version info
  /tools    List all available tools with descriptions`
}

func helpText() string {
	return `ccx-go — AI coding assistant

Commands:
  /help     Show this help
  /login    Log in to Claude via OAuth
  /exit     Quit the session
  /clear    Clear the screen
  /cost     Show token usage and cost
  /model    Show current model
  /compact  Compress conversation context
  /version  Show version info
  /tools    List available tools

Shortcuts:
  Ctrl+C    Quit
  Enter     Send message`
}

func costOutput(t *cost.Tracker) string {
	if t == nil {
		return "No cost data available."
	}
	input, output := t.TotalTokens()
	totalCost := t.TotalCost()
	calls := t.CallCount()
	return fmt.Sprintf("Session cost:\n  API calls:     %d\n  Input tokens:  %d\n  Output tokens: %d\n  Total cost:    $%.4f", calls, input, output, totalCost)
}

func toolsOutput(r *tool.Registry) string {
	if r == nil {
		return "No tools registered."
	}
	tools := r.All()
	if len(tools) == 0 {
		return "No tools registered."
	}

	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Name() < tools[j].Name()
	})

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Available tools (%d):\n", len(tools)))
	for _, t := range tools {
		desc := t.Description()
		if len(desc) > 60 {
			desc = desc[:57] + "..."
		}
		b.WriteString(fmt.Sprintf("  %-16s %s\n", t.Name(), desc))
	}
	return strings.TrimRight(b.String(), "\n")
}
