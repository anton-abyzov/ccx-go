package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// Inline rendering styles (Claude Code-like)
var (
	accentStyle = lipgloss.NewStyle().
			Foreground(orange)

	promptCharStyle = lipgloss.NewStyle().
			Foreground(orange).
			Bold(true)

	userMsgStyle = lipgloss.NewStyle().
			Bold(true).
			Background(lipgloss.Color("237"))

	toolDotStyle = lipgloss.NewStyle().
			Foreground(green)

	toolBoldStyle = lipgloss.NewStyle().
			Bold(true)

	toolDimStyle = lipgloss.NewStyle().
			Foreground(dimGray)

	separatorDimStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("238"))

	welcomeBoldStyle = lipgloss.NewStyle().
				Bold(true)

	welcomeGreenStyle = lipgloss.NewStyle().
				Foreground(green)

	welcomeOrangeStyle = lipgloss.NewStyle().
				Foreground(orange).
				Bold(true)

	welcomeDim = lipgloss.NewStyle().
			Foreground(dimGray)

	errorInlineStyle = lipgloss.NewStyle().
				Foreground(red).
				Bold(true)
)

const inlinePet = `   ╭─────────╮
   │  ◉   ◉  │
   │    ◡    │
   ╰─────────╯`

// termWidth returns the current terminal width, defaulting to 80.
func termWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		return 80
	}
	return w
}

// RenderWelcomeInline prints the welcome panel inline (not full-screen).
func RenderWelcomeInline(version, model, cwd, authDisplay string, toolCount int) {
	w := termWidth()
	if w > 100 {
		w = 100
	}

	// Header rule
	title := fmt.Sprintf(" CCX-GO v%s ", version)
	titleLen := len(title)
	leftPad := 6
	rightPad := w - leftPad - titleLen
	if rightPad < 0 {
		rightPad = 0
	}
	header := accentStyle.Render(strings.Repeat("─", leftPad) + title + strings.Repeat("─", rightPad))
	fmt.Println(header)
	fmt.Println()

	// Bordered welcome panel
	name := getUserFirstName()
	shortCwd := shortenPath(cwd)
	modelShort := shortModelName(model)

	// Build left content
	leftLines := []string{
		welcomeBoldStyle.Render(fmt.Sprintf("Welcome back, %s!", name)),
		"",
	}
	for _, line := range strings.Split(inlinePet, "\n") {
		leftLines = append(leftLines, line)
	}
	leftLines = append(leftLines, "")
	authLine := welcomeGreenStyle.Render(modelShort) + " · " + authDisplay
	leftLines = append(leftLines, authLine)
	leftLines = append(leftLines, welcomeDim.Render(shortCwd))
	leftLines = append(leftLines, welcomeDim.Render(fmt.Sprintf("%d tools available", toolCount)))

	// Build right content
	rightLines := []string{
		welcomeOrangeStyle.Render("Tips for getting started"),
		"",
		welcomeDim.Render("Ask me to help you understand,"),
		welcomeDim.Render("fix, or improve your code."),
		"",
		welcomeOrangeStyle.Render("Recent activity"),
		welcomeDim.Render("No recent activity"),
	}

	// Calculate panel widths
	innerW := w - 4 // border takes 2 per side
	leftW := innerW * 55 / 100
	rightW := innerW - leftW - 3 // 3 for divider with padding

	// Pad lines to equal height
	maxLines := len(leftLines)
	if len(rightLines) > maxLines {
		maxLines = len(rightLines)
	}
	for len(leftLines) < maxLines {
		leftLines = append(leftLines, "")
	}
	for len(rightLines) < maxLines {
		rightLines = append(rightLines, "")
	}

	// Top border
	fmt.Println(accentStyle.Render("  ┌" + strings.Repeat("─", innerW+2) + "┐"))

	// Content rows
	for i := 0; i < maxLines; i++ {
		left := padRight(leftLines[i], leftW)
		right := padRight(rightLines[i], rightW)
		divider := separatorDimStyle.Render("│")
		fmt.Printf("  %s %s %s %s %s\n",
			accentStyle.Render("│"),
			left,
			divider,
			right,
			accentStyle.Render("│"),
		)
	}

	// Bottom border
	fmt.Println(accentStyle.Render("  └" + strings.Repeat("─", innerW+2) + "┘"))
	fmt.Println()
}

// RenderPromptInline prints the ❯ prompt character.
func RenderPromptInline() {
	fmt.Print(promptCharStyle.Render("❯") + " ")
}

// RenderUserMessageInline prints the user's message with styled background.
func RenderUserMessageInline(text string) {
	fmt.Println(userMsgStyle.Render(fmt.Sprintf(" ❯ %s ", text)))
	fmt.Println()
}

// RenderToolStartInline prints tool execution start (green dot + bold name).
func RenderToolStartInline(name, detail string) {
	dot := toolDotStyle.Render("●")
	label := name
	if detail != "" {
		label = fmt.Sprintf("%s(%s)", name, detail)
	}
	nm := toolBoldStyle.Render(label)
	fmt.Printf("  %s %s\n", dot, nm)
}

// RenderToolOutputInline prints tool output with dim styling and collapsible display.
// Outputs longer than 5 lines show first 3 lines + a "[+N more lines]" summary.
func RenderToolOutputInline(output string, isError bool) {
	if output == "" {
		return
	}

	style := toolDimStyle
	if isError {
		style = errorInlineStyle
	}

	lines := strings.Split(output, "\n")
	if len(lines) > 5 {
		hidden := len(lines) - 3
		for _, line := range lines[:3] {
			fmt.Printf("  %s\n", style.Render("└ "+line))
		}
		fmt.Printf("  %s\n", toolDimStyle.Render(fmt.Sprintf("  [+%d more lines]", hidden)))
	} else {
		for _, line := range lines {
			fmt.Printf("  %s\n", style.Render("└ "+line))
		}
	}
}

// RenderSeparator prints a dim horizontal rule.
func RenderSeparator() {
	w := termWidth()
	if w > 100 {
		w = 100
	}
	fmt.Println()
	fmt.Println(separatorDimStyle.Render(strings.Repeat("─", w)))
	fmt.Println()
}

// RenderStreamChunk prints a streaming text chunk without newline (for progressive rendering).
func RenderStreamChunk(text string) {
	fmt.Print(text)
}

// RenderStreamEnd finishes a streaming block.
func RenderStreamEnd() {
	fmt.Println()
}

// RenderErrorInline prints an error message.
func RenderErrorInline(err error) {
	fmt.Printf("  %s %s\n", errorInlineStyle.Render("✗"), errorInlineStyle.Render(err.Error()))
}

// RenderWorkingInline prints the "● Working..." indicator on the current line (no newline).
func RenderWorkingInline() {
	fmt.Printf("  %s Working...", toolDotStyle.Render("●"))
}

// RenderWorkingClearInline clears the working indicator using \r + ANSI clear-to-end.
func RenderWorkingClearInline() {
	fmt.Print("\r\033[K")
}

// RenderMarkdownInline renders text with glamour markdown formatting and prints it.
func RenderMarkdownInline(text string) {
	if text == "" {
		return
	}
	w := termWidth()
	rendered := renderMarkdown(text, w)
	fmt.Print(rendered)
}

// RenderFooterInline prints the footer bar: "? for shortcuts" (left) + "● model" (right).
func RenderFooterInline(model string) {
	w := termWidth()
	if w > 100 {
		w = 100
	}
	left := welcomeDim.Render("? for shortcuts")
	modelShort := shortModelName(model)
	right := welcomeDim.Render(fmt.Sprintf("● %s", modelShort))
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}
	fmt.Println(left + strings.Repeat(" ", gap) + right)
	fmt.Println()
}

// RenderCommandOutputInline prints slash command output with dim styling.
func RenderCommandOutputInline(output string) {
	fmt.Println(welcomeDim.Render(output))
}

// padRight pads a string with spaces to reach the target visible width.
func padRight(s string, width int) string {
	visW := lipgloss.Width(s)
	if visW >= width {
		return s
	}
	return s + strings.Repeat(" ", width-visW)
}
