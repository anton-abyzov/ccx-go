package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/anton-abyzov/ccx-go/internal/api"
	"github.com/anton-abyzov/ccx-go/internal/compact"
	"github.com/anton-abyzov/ccx-go/internal/config"
	"github.com/anton-abyzov/ccx-go/internal/cost"
	"github.com/anton-abyzov/ccx-go/internal/prompt"
	"github.com/anton-abyzov/ccx-go/internal/query"
	"github.com/anton-abyzov/ccx-go/internal/skill"
	"github.com/anton-abyzov/ccx-go/internal/tool"
	agentTool "github.com/anton-abyzov/ccx-go/internal/tools/agent"
	"github.com/anton-abyzov/ccx-go/internal/tools/bash"
	"github.com/anton-abyzov/ccx-go/internal/tools/fileedit"
	"github.com/anton-abyzov/ccx-go/internal/tools/fileread"
	"github.com/anton-abyzov/ccx-go/internal/tools/filewrite"
	"github.com/anton-abyzov/ccx-go/internal/tools/glob"
	"github.com/anton-abyzov/ccx-go/internal/tools/grep"
	"github.com/anton-abyzov/ccx-go/internal/tools/notebookedit"
	"github.com/anton-abyzov/ccx-go/internal/tools/planmode"
	"github.com/anton-abyzov/ccx-go/internal/tools/sendmessage"
	"github.com/anton-abyzov/ccx-go/internal/tools/taskcreate"
	"github.com/anton-abyzov/ccx-go/internal/tools/tasklist"
	"github.com/anton-abyzov/ccx-go/internal/tools/taskupdate"
	"github.com/anton-abyzov/ccx-go/internal/tools/teamcreate"
	"github.com/anton-abyzov/ccx-go/internal/tools/teamdelete"
	"github.com/anton-abyzov/ccx-go/internal/tools/todowrite"
	"github.com/anton-abyzov/ccx-go/internal/tools/webfetch"
	"github.com/anton-abyzov/ccx-go/internal/tools/websearch"
	"github.com/anton-abyzov/ccx-go/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var version = "dev"

func main() {
	rootCmd := &cobra.Command{
		Use:     "ccx-go [prompt]",
		Short:   "AI coding assistant CLI",
		Long:    "ccx-go — a Go implementation of an AI coding assistant powered by Claude.",
		Version: version,
		RunE:    runQuery,
		Args:    cobra.ArbitraryArgs,
	}

	rootCmd.Flags().StringP("model", "m", "claude-sonnet-4-20250514", "model to use")
	rootCmd.Flags().Int("max-turns", 200, "maximum conversation turns")
	rootCmd.Flags().Int("max-tokens", 16384, "maximum tokens per response")
	rootCmd.Flags().Bool("no-stream", false, "disable streaming output")
	rootCmd.Flags().StringP("system", "s", "", "additional system prompt")
	rootCmd.Flags().Bool("tui", false, "use full-screen Bubbletea TUI (default: inline)")
	rootCmd.Flags().Bool("dangerously-skip-permissions", true, "skip all permission prompts (default)")
	rootCmd.Flags().String("permission-mode", "bypass", "permission mode: default, acceptEdits, bypass, plan")
	rootCmd.Flags().String("provider", "anthropic", "API provider: anthropic, openrouter")
	rootCmd.Flags().String("openrouter-key", "", "OpenRouter API key (env: OPENROUTER_API_KEY)")

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runQuery(cmd *cobra.Command, args []string) error {
	provider, _ := cmd.Flags().GetString("provider")
	model, _ := cmd.Flags().GetString("model")
	maxTurns, _ := cmd.Flags().GetInt("max-turns")
	maxTokens, _ := cmd.Flags().GetInt("max-tokens")
	extraSystem, _ := cmd.Flags().GetString("system")
	useTUI, _ := cmd.Flags().GetBool("tui")
	openrouterKey, _ := cmd.Flags().GetString("openrouter-key")

	// Load configuration
	settings, _ := config.LoadSettings(config.DefaultSettingsPath())
	if settings == nil {
		settings = &config.Settings{}
	}
	if settings.Model != "" && !cmd.Flags().Changed("model") {
		model = settings.Model
	}
	if settings.MaxTokens > 0 && !cmd.Flags().Changed("max-tokens") {
		maxTokens = settings.MaxTokens
	}

	// Discover CLAUDE.md files
	cwd, _ := os.Getwd()
	claudeMD := config.DiscoverClaudeMD(cwd)

	// Initialize cost tracker
	tracker := cost.NewTracker()

	// Set up client based on provider
	var client api.MessageClient
	var authDisplay string

	switch provider {
	case "openrouter":
		if openrouterKey == "" {
			openrouterKey = os.Getenv("OPENROUTER_API_KEY")
		}
		if openrouterKey == "" {
			return fmt.Errorf("OpenRouter requires --openrouter-key or OPENROUTER_API_KEY env var")
		}
		client = api.NewOpenAIClient(openrouterKey)
		authDisplay = "OpenRouter"
		if !cmd.Flags().Changed("model") {
			model = "nvidia/nemotron-3-super-120b-a12b:free"
		}
	default:
		auth, err := config.ResolveAuth()
		if err != nil {
			return fmt.Errorf("%w", err)
		}
		anthropicClient := api.NewClient(auth.Key)
		if auth.IsOAuth {
			anthropicClient.WithOAuth(true)
		}
		client = anthropicClient
		authDisplay = auth.Display
		if auth.Email != "" {
			authDisplay = auth.Display + " (" + auth.Email + ")"
		}
	}

	registry := registerTools(cwd, client, model)

	// Build CLAUDE.md content for system prompt
	var claudeMDContent string
	if claudeMD != nil && len(claudeMD.Entries) > 0 {
		var parts []string
		for _, entry := range claudeMD.Entries {
			parts = append(parts, fmt.Sprintf("## %s\n%s", entry.Path, entry.Content))
		}
		claudeMDContent = strings.Join(parts, "\n\n")
	}

	// Add settings system prompt and memory
	var extraParts []string
	if settings.SystemPrompt != "" {
		extraParts = append(extraParts, settings.SystemPrompt)
	}
	if extraSystem != "" {
		extraParts = append(extraParts, extraSystem)
	}
	memDir := config.MemoryDir()
	if memIndex, err := config.LoadMemoryIndex(memDir); err == nil && memIndex != "" {
		extraParts = append(extraParts, "# Memory\n"+memIndex)
	}

	// Build system prompt using the prompt package
	systemPrompt := prompt.BuildSystemPrompt(
		registry.All(),
		claudeMDContent,
		cwd,
		strings.Join(extraParts, "\n\n"),
	)

	// Enable agent log output
	agentTool.SetLogWriter(os.Stderr)

	// Detect interactive TTY vs piped input
	isTTY := term.IsTerminal(int(os.Stdin.Fd()))

	if isTTY {
		if useTUI {
			return runFullscreenTUI(cmd.Context(), client, registry, cwd, model, maxTokens, maxTurns, systemPrompt, tracker, args)
		}
		return runInline(cmd.Context(), client, registry, cwd, model, authDisplay, maxTokens, maxTurns, systemPrompt, tracker, args)
	}

	return runPipe(cmd.Context(), client, registry, model, maxTokens, maxTurns, systemPrompt, tracker, args)
}

// runInline runs the Claude Code-style inline chat (default for TTY).
func runInline(parentCtx context.Context, client api.MessageClient, registry *tool.Registry, cwd, model, authDisplay string, maxTokens, maxTurns int, systemPrompt string, tracker *cost.Tracker, args []string) error {
	ctx, cancel := signal.NotifyContext(parentCtx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Print welcome panel + footer
	tui.RenderWelcomeInline(version, model, cwd, authDisplay, registry.Count())
	tui.RenderFooterInline(model)

	// Streaming state managed via closures
	streaming := false
	working := false
	var streamBuf strings.Builder

	loop := query.NewLoop(client, registry, query.LoopConfig{
		Model:          model,
		MaxTokens:      maxTokens,
		MaxTurns:       maxTurns,
		System:         systemPrompt,
		SuppressOutput: true,
		OnText: func(text string) {
			if working {
				tui.RenderWorkingClearInline()
				working = false
			}
			if !streaming {
				streaming = true
			}
			streamBuf.WriteString(text)
		},
		OnThinking: func(text string) {
			if working {
				tui.RenderWorkingClearInline()
				working = false
			}
			tui.RenderThinkingInline(text)
		},
		OnToolStart: func(name string) {
			if working {
				tui.RenderWorkingClearInline()
				working = false
			}
			if streaming {
				tui.RenderMarkdownInline(streamBuf.String())
				streamBuf.Reset()
				streaming = false
			}
			tui.RenderToolStartInline(name, "")
		},
		OnToolDone: func(name, result string, isErr bool) {
			tui.RenderToolOutputInline(result, isErr)
		},
		OnTurnComplete: func(usage api.Usage) {
			tracker.Record(model, usage.InputTokens, usage.OutputTokens)
		},
	})

	cmdCtx := tui.CommandContext{
		Version:  version,
		Model:    model,
		CWD:      cwd,
		Tracker:  tracker,
		Registry: registry,
	}

	// sendAndRender handles showing working indicator, calling the API, and rendering the result.
	// apiInput is sent to the model; displayText is shown to the user (if non-empty).
	sendAndRender := func(apiInput, displayText string) {
		if displayText != "" {
			tui.RenderUserMessageInline(displayText)
		}
		streaming = false
		working = true
		streamBuf.Reset()
		tui.RenderWorkingInline()

		if err := loop.SendMessage(ctx, apiInput); err != nil {
			if working {
				tui.RenderWorkingClearInline()
				working = false
			}
			tui.RenderErrorInline(err)
		}
		if working {
			tui.RenderWorkingClearInline()
			working = false
		}
		if streaming {
			tui.RenderMarkdownInline(streamBuf.String())
			streamBuf.Reset()
			streaming = false
		}
		tui.RenderSeparator()
	}

	// Handle initial prompt from args
	if len(args) > 0 {
		initialPrompt := strings.Join(args, " ")
		sendAndRender(initialPrompt, initialPrompt)
	}

	// Discover skills for tab completion and invocation
	discoveredSkills := skill.DiscoverAll()
	var skillCompletions []string
	for _, s := range discoveredSkills {
		skillCompletions = append(skillCompletions, "/"+s.Name)
	}

	line := tui.NewReadline(skillCompletions)
	defer line.Close()
	defer tui.SaveHistory(line)

	for {
		input, err := line.Prompt("❯ ")
		if err != nil {
			break // Ctrl+C or Ctrl+D
		}
		// Clear liner's plain-text echo so only the styled version shows
		fmt.Print("\033[A\033[2K\r")
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		line.AppendHistory(input)

		// Handle slash commands
		if strings.HasPrefix(input, "/") {
			result := tui.HandleSlashCommand(input, cmdCtx)
			if result != nil {
				tui.RenderUserMessageInline(input)
				if result.Quit {
					break
				}
				if result.Compact {
					loop.Messages = compact.MicroCompact(loop.Messages, 4)
				}
				if result.Output != "" {
					tui.RenderCommandOutputInline(result.Output)
				}
				fmt.Println()
				continue
			}

			// Try skill invocation
			parts := strings.SplitN(input, " ", 2)
			skillName := strings.TrimPrefix(parts[0], "/")
			skillArgs := ""
			if len(parts) > 1 {
				skillArgs = parts[1]
			}
			if s := skill.FindSkillByName(discoveredSkills, skillName); s != nil {
				skillPrompt := fmt.Sprintf("<skill name=%q>\n%s\n</skill>", s.Name, s.Body)
				if skillArgs != "" {
					skillPrompt += fmt.Sprintf("\n\nUser request: %s", skillArgs)
				}
				sendAndRender(skillPrompt, input)
				continue
			}

			// Unknown command
			tui.RenderUserMessageInline(input)
			tui.RenderCommandOutputInline(fmt.Sprintf("Unknown command: %s\nType / for available commands.", parts[0]))
			fmt.Println()
			continue
		}

		sendAndRender(input, input)
	}

	fmt.Fprintf(os.Stderr, "\n--- session: %s ---\n", tracker.Summary())
	return nil
}

// runFullscreenTUI runs the original Bubbletea full-screen mode (--tui flag).
func runFullscreenTUI(parentCtx context.Context, client api.MessageClient, registry *tool.Registry, cwd, model string, maxTokens, maxTurns int, systemPrompt string, tracker *cost.Tracker, args []string) error {
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	msgCh := make(chan tea.Msg, 100)

	loop := query.NewLoop(client, registry, query.LoopConfig{
		Model:          model,
		MaxTokens:      maxTokens,
		MaxTurns:       maxTurns,
		System:         systemPrompt,
		SuppressOutput: true,
		OnText: func(text string) {
			msgCh <- tui.StreamTextMsg{Text: text}
		},
		OnThinking: func(text string) {
			msgCh <- tui.StreamThinkingMsg{Text: text}
		},
		OnToolStart: func(name string) {
			msgCh <- tui.ToolExecMsg{ToolName: name}
		},
		OnToolDone: func(name, result string, isErr bool) {
			msgCh <- tui.ToolDoneMsg{ToolName: name, Content: result, IsError: isErr}
		},
		OnTurnComplete: func(usage api.Usage) {
			tracker.Record(model, usage.InputTokens, usage.OutputTokens)
		},
	})

	onSubmit := func(text string) {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					msgCh <- tui.ErrorMsg{Err: fmt.Errorf("panic: %v", r)}
					return
				}
			}()

			err := loop.SendMessage(ctx, text)
			if err != nil {
				msgCh <- tui.ErrorMsg{Err: err}
				return
			}
			msgCh <- tui.DoneMsg{}
		}()
	}

	// Build initial prompt from args
	initialPrompt := ""
	if len(args) > 0 {
		initialPrompt = strings.Join(args, " ")
	}

	m := tui.New(tui.AppConfig{
		ModelName:     model,
		Version:       version,
		CWD:           cwd,
		InitialPrompt: initialPrompt,
	}, msgCh, onSubmit)

	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()

	// Print session summary after TUI exits
	fmt.Fprintf(os.Stderr, "\n--- session: %s ---\n", tracker.Summary())

	return err
}

func runPipe(parentCtx context.Context, client api.MessageClient, registry *tool.Registry, model string, maxTokens, maxTurns int, systemPrompt string, tracker *cost.Tracker, args []string) error {
	prompt := ""
	if len(args) > 0 {
		prompt = strings.Join(args, " ")
	}

	// For piped single-line input, read it early so we can check for slash commands
	if prompt == "" {
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			prompt = strings.TrimSpace(scanner.Text())
		}
		if prompt == "" {
			return nil
		}
	}

	// Handle slash commands without hitting the API
	if strings.HasPrefix(prompt, "/") {
		cwd, _ := os.Getwd()
		cmdCtx := tui.CommandContext{
			Version:  version,
			Model:    model,
			CWD:      cwd,
			Tracker:  tracker,
			Registry: registry,
		}
		result := tui.HandleSlashCommand(prompt, cmdCtx)
		if result != nil {
			if result.Output != "" {
				fmt.Println(result.Output)
			}
			return nil
		}

		// Try skill invocation
		discoveredSkills := skill.DiscoverAll()
		parts := strings.SplitN(prompt, " ", 2)
		skillName := strings.TrimPrefix(parts[0], "/")
		skillArgs := ""
		if len(parts) > 1 {
			skillArgs = parts[1]
		}
		if s := skill.FindSkillByName(discoveredSkills, skillName); s != nil {
			prompt = fmt.Sprintf("<skill name=%q>\n%s\n</skill>", s.Name, s.Body)
			if skillArgs != "" {
				prompt += fmt.Sprintf("\n\nUser request: %s", skillArgs)
			}
		}
	}

	loop := query.NewLoop(client, registry, query.LoopConfig{
		Model:     model,
		MaxTokens: maxTokens,
		MaxTurns:  maxTurns,
		System:    systemPrompt,
		OnTurnComplete: func(usage api.Usage) {
			tracker.Record(model, usage.InputTokens, usage.OutputTokens)
		},
	})

	ctx, cancel := signal.NotifyContext(parentCtx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	err := loop.SendMessage(ctx, prompt)

	fmt.Fprintf(os.Stderr, "\n--- session: %s ---\n", tracker.Summary())

	return err
}

func registerTools(cwd string, client api.MessageClient, model string) *tool.Registry {
	registry := tool.NewRegistry()

	agentSystem := "You are a sub-agent. Complete the given task using available tools, then return a concise summary."

	tools := []tool.Tool{
		bash.New(cwd),
		fileread.New(),
		filewrite.New(),
		fileedit.New(),
		glob.New(),
		grep.New(),
		webfetch.New(),
		websearch.New(),
		todowrite.New(),
		notebookedit.New(),
		agentTool.New(client, registry, model, agentSystem),
		teamcreate.New(),
		teamdelete.New(),
		sendmessage.New(),
		taskcreate.New(),
		taskupdate.New(),
		tasklist.New(),
		planmode.NewEnter(),
		planmode.NewExit(),
	}

	for _, t := range tools {
		if err := registry.Register(t); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to register tool %s: %v\n", t.Name(), err)
		}
	}

	return registry
}
