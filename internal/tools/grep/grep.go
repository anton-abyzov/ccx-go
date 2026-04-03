package grep

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/anton-abyzov/ccx-go/internal/tool"
)

type input struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	Glob       string `json:"glob,omitempty"`
	Type       string `json:"type,omitempty"`
	OutputMode string `json:"output_mode,omitempty"`
	Context    int    `json:"context,omitempty"`
	Before     int    `json:"-B,omitempty"`
	After      int    `json:"-A,omitempty"`
	Insensitive bool  `json:"-i,omitempty"`
	Multiline  bool   `json:"multiline,omitempty"`
	HeadLimit  int    `json:"head_limit,omitempty"`
	LineNumbers bool  `json:"-n,omitempty"`
}

// Tool implements search via ripgrep with grep fallback.
type Tool struct{}

func New() *Tool { return &Tool{} }

func (t *Tool) Name() string        { return "Grep" }
func (t *Tool) Description() string { return "Search file contents using ripgrep." }

func (t *Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "Regex pattern to search for"},
			"path": {"type": "string", "description": "Directory or file to search"},
			"glob": {"type": "string", "description": "Glob filter (e.g. *.go)"},
			"type": {"type": "string", "description": "File type filter (e.g. go, py, js)"},
			"output_mode": {"type": "string", "enum": ["content", "files_with_matches", "count"], "description": "Output mode (default: files_with_matches)"},
			"context": {"type": "integer", "description": "Lines of context around matches (-C)"},
			"-A": {"type": "integer", "description": "Lines after each match"},
			"-B": {"type": "integer", "description": "Lines before each match"},
			"-i": {"type": "boolean", "description": "Case-insensitive search"},
			"-n": {"type": "boolean", "description": "Show line numbers (default true for content mode)"},
			"multiline": {"type": "boolean", "description": "Enable multiline matching"},
			"head_limit": {"type": "integer", "description": "Limit output to first N lines (default 250)"}
		},
		"required": ["pattern"]
	}`)
}

func (t *Tool) Execute(ctx context.Context, rawInput json.RawMessage) (*tool.Result, error) {
	var in input
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return &tool.Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}

	if in.Pattern == "" {
		return &tool.Result{Content: "pattern is required", IsError: true}, nil
	}

	// Default head_limit
	if in.HeadLimit == 0 {
		in.HeadLimit = 250
	}

	args := buildArgs(in)

	cmd := exec.CommandContext(ctx, "rg", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	// rg exits with 1 when no matches found - that's ok
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return &tool.Result{Content: "no matches found"}, nil
		}
		// Check if rg is not installed, fall back to grep
		if isNotFound(err) {
			return t.fallbackGrep(ctx, in)
		}
		errMsg := stderr.String()
		if errMsg == "" {
			errMsg = err.Error()
		}
		return &tool.Result{Content: fmt.Sprintf("search error: %s", errMsg), IsError: true}, nil
	}

	output := stdout.String()
	if output == "" {
		return &tool.Result{Content: "no matches found"}, nil
	}

	output = strings.TrimRight(output, "\n")

	// Apply head_limit
	if in.HeadLimit > 0 {
		output = limitLines(output, in.HeadLimit)
	}

	return &tool.Result{Content: output}, nil
}

func buildArgs(in input) []string {
	var args []string

	switch in.OutputMode {
	case "files_with_matches", "":
		args = append(args, "-l")
	case "count":
		args = append(args, "-c")
	default:
		// content mode with line numbers by default
		args = append(args, "-n")
	}

	if in.Insensitive {
		args = append(args, "-i")
	}

	if in.Multiline {
		args = append(args, "-U", "--multiline-dotall")
	}

	if in.Context > 0 {
		args = append(args, "-C", fmt.Sprintf("%d", in.Context))
	}
	if in.Before > 0 {
		args = append(args, "-B", fmt.Sprintf("%d", in.Before))
	}
	if in.After > 0 {
		args = append(args, "-A", fmt.Sprintf("%d", in.After))
	}

	if in.Glob != "" {
		args = append(args, "--glob", in.Glob)
	}

	if in.Type != "" {
		args = append(args, "--type", in.Type)
	}

	// Limit max count for files_with_matches and count modes
	if in.HeadLimit > 0 && (in.OutputMode == "files_with_matches" || in.OutputMode == "" || in.OutputMode == "count") {
		args = append(args, "--max-count", "1")
	}

	args = append(args, in.Pattern)

	if in.Path != "" {
		args = append(args, in.Path)
	} else {
		args = append(args, ".")
	}

	return args
}

func (t *Tool) fallbackGrep(ctx context.Context, in input) (*tool.Result, error) {
	args := []string{"-rn"}
	if in.Insensitive {
		args = append(args, "-i")
	}
	args = append(args, in.Pattern)
	if in.Path != "" {
		args = append(args, in.Path)
	} else {
		args = append(args, ".")
	}

	cmd := exec.CommandContext(ctx, "grep", args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return &tool.Result{Content: "no matches found"}, nil
		}
		return &tool.Result{Content: fmt.Sprintf("search error: %v", err), IsError: true}, nil
	}

	output := stdout.String()
	if output == "" {
		return &tool.Result{Content: "no matches found"}, nil
	}

	output = strings.TrimRight(output, "\n")
	if in.HeadLimit > 0 {
		output = limitLines(output, in.HeadLimit)
	}

	return &tool.Result{Content: output}, nil
}

// limitLines returns at most n lines from the string.
func limitLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n... (%d more lines)", len(lines)-n)
}

func isNotFound(err error) bool {
	if _, ok := err.(*exec.Error); ok {
		return true
	}
	return false
}
