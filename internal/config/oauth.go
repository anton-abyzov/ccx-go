package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	oauthClientID    = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	oauthAuthorizeURL = "https://claude.ai/oauth/authorize"
	oauthTokenURL     = "https://platform.claude.com/v1/oauth/token"
	oauthScope        = "user:profile user:inference"
	keychainService   = "Claude Code-credentials"
)

// RunOAuthLogin performs the full OAuth PKCE login flow:
// generates PKCE pair, starts local callback server, opens browser,
// exchanges code for token, and saves credentials.
func RunOAuthLogin() error {
	// 1. Generate PKCE code verifier and challenge
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return fmt.Errorf("generating PKCE: %w", err)
	}

	// 2. Start local HTTP server on random port
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return fmt.Errorf("starting callback server: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://localhost:%d/oauth/callback", port)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errMsg := r.URL.Query().Get("error")
			if errMsg == "" {
				errMsg = "no authorization code received"
			}
			http.Error(w, errMsg, http.StatusBadRequest)
			errCh <- fmt.Errorf("oauth callback: %s", errMsg)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body><h2>Login successful!</h2><p>You can close this tab.</p></body></html>")
		codeCh <- code
	})

	server := &http.Server{Handler: mux}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("callback server: %w", err)
		}
	}()

	// 3. Open browser to authorization URL
	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {oauthClientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {oauthScope},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	authURL := oauthAuthorizeURL + "?" + params.Encode()

	fmt.Println("Opening browser for authentication...")
	if err := exec.Command("open", authURL).Start(); err != nil {
		fmt.Printf("Could not open browser. Visit this URL manually:\n%s\n", authURL)
	}

	// 4. Wait for authorization code
	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		server.Close()
		return err
	}
	server.Close()

	// 5. Exchange code for token
	tokenJSON, err := exchangeCodeForToken(code, redirectURI, verifier)
	if err != nil {
		return fmt.Errorf("token exchange: %w", err)
	}

	// 6. Save credentials
	if err := saveCredentials(tokenJSON); err != nil {
		return fmt.Errorf("saving credentials: %w", err)
	}

	fmt.Println("Login successful!")
	return nil
}

// generatePKCE creates a PKCE code_verifier and code_challenge.
func generatePKCE() (verifier, challenge string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)

	hash := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(hash[:])

	return verifier, challenge, nil
}

// exchangeCodeForToken POSTs the authorization code to the token endpoint
// and returns the raw credentials JSON suitable for storage.
func exchangeCodeForToken(code, redirectURI, verifier string) (string, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {oauthClientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}

	resp, err := http.PostForm(oauthTokenURL, data)
	if err != nil {
		return "", fmt.Errorf("POST token endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	// Parse the token response
	var tokenResp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("parsing token response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("no access_token in response: %s", string(body))
	}

	// Build credentials JSON in the format ResolveAuth expects
	creds := map[string]any{
		"claudeAiOauth": map[string]string{
			"accessToken": tokenResp.AccessToken,
		},
	}
	credJSON, err := json.Marshal(creds)
	if err != nil {
		return "", fmt.Errorf("marshaling credentials: %w", err)
	}

	return string(credJSON), nil
}

// saveCredentials stores the token in macOS Keychain and ~/.claude/.credentials.json.
func saveCredentials(tokenJSON string) error {
	// Save to macOS Keychain
	cmd := exec.Command("security", "add-generic-password",
		"-a", "default",
		"-s", keychainService,
		"-w", tokenJSON,
		"-U",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: keychain save failed: %v (%s)\n", err, strings.TrimSpace(string(out)))
	}

	// Save to credentials file
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home dir: %w", err)
	}

	credDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(credDir, 0700); err != nil {
		return fmt.Errorf("creating .claude dir: %w", err)
	}

	credPath := filepath.Join(credDir, ".credentials.json")
	if err := os.WriteFile(credPath, []byte(tokenJSON), 0600); err != nil {
		return fmt.Errorf("writing credentials file: %w", err)
	}

	return nil
}
