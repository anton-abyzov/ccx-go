package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
)

// ChatMessage represents a displayable message.
type ChatMessage struct {
	Role    string // "user", "assistant", "tool", "error"
	Content string
	ToolID  string // for tool results
}

// RenderMessage formats a chat message for display.
func RenderMessage(msg ChatMessage, width int) string {
	var b strings.Builder

	switch msg.Role {
	case "user":
		b.WriteString(userStyle.Render("❯ " + msg.Content))
		b.WriteString("\n\n")

	case "assistant":
		rendered := renderMarkdown(msg.Content, width)
		b.WriteString(rendered)

	case "tool":
		header := fmt.Sprintf("%s %s", toolDotStyle.Render("●"), toolBoldStyle.Render(msg.ToolID))
		b.WriteString(header)
		b.WriteString("\n")
		if msg.Content != "" {
			content := msg.Content
			if len(content) > 2000 {
				content = content[:2000] + "\n... (truncated)"
			}
			b.WriteString(toolOutputStyle.Render(content))
			b.WriteString("\n")
		}
		b.WriteString("\n")

	case "error":
		b.WriteString(errorStyle.Render("✗ " + msg.Content))
		b.WriteString("\n\n")
	}

	return b.String()
}

func renderMarkdown(text string, width int) string {
	if width <= 0 {
		width = 80
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width-4),
	)
	if err != nil {
		return text
	}

	out, err := r.Render(text)
	if err != nil {
		return text
	}
	return out
}
