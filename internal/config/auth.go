package config

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// AuthResult holds the resolved authentication credentials.
type AuthResult struct {
	Key     string // API key or OAuth access token
	IsOAuth bool   // true if token is from OAuth (needs Bearer auth)
	Display string // human-readable auth method for UI
	Email   string // account email (from OAuth credentials, if available)
}

// ResolveAuth tries multiple auth sources in priority order:
// 1. ANTHROPIC_API_KEY env var
// 2. macOS Keychain (Claude Code credentials)
// 3. ~/.claude/.credentials.json file
func ResolveAuth() (*AuthResult, error) {
	// 1. Environment variable
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return &AuthResult{Key: key, IsOAuth: false, Display: "API Key"}, nil
	}

	// 2. macOS Keychain
	if runtime.GOOS == "darwin" {
		if token, subType, email := readKeychain(); token != "" {
			return &AuthResult{Key: token, IsOAuth: true, Display: "Claude " + subType, Email: email}, nil
		}
	}

	// 3. Credentials file fallback
	if token, subType, email := readCredentialsFile(); token != "" {
		return &AuthResult{Key: token, IsOAuth: true, Display: "Claude " + subType, Email: email}, nil
	}

	return nil, errors.New("no authentication found.\n\nSet ANTHROPIC_API_KEY or log in with: claude auth")
}

func readKeychain() (string, string, string) {
	user := os.Getenv("USER")
	if user == "" {
		return "", "", ""
	}
	cmd := exec.Command("security", "find-generic-password", "-a", user, "-w", "-s", "Claude Code-credentials")
	out, err := cmd.Output()
	if err != nil {
		return "", "", ""
	}
	return parseOAuthJSON(strings.TrimSpace(string(out)))
}

func readCredentialsFile() (string, string, string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	if err != nil {
		return "", "", ""
	}
	return parseOAuthJSON(string(data))
}

// parseOAuthJSON extracts the access token, subscription type, and email from
// the Claude Code credentials JSON structure.
func parseOAuthJSON(raw string) (string, string, string) {
	var creds struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
		ClaudeAiSubscriptionType string `json:"claudeAiSubscriptionType"`
		Email                    string `json:"email"`
	}
	if err := json.Unmarshal([]byte(raw), &creds); err != nil {
		return "", "", ""
	}
	token := creds.ClaudeAiOauth.AccessToken
	if token == "" {
		return "", "", ""
	}
	subType := creds.ClaudeAiSubscriptionType
	if subType == "" {
		subType = "Pro"
	}
	if len(subType) > 0 {
		subType = strings.ToUpper(subType[:1]) + subType[1:]
	}
	return token, subType, creds.Email
}
