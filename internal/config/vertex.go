package config

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// VertexConfig holds Google Cloud Vertex AI configuration.
type VertexConfig struct {
	Region    string
	ProjectID string
	Token     string // GCP access token
}

// ResolveVertex checks environment variables for Vertex AI configuration.
// Returns nil if CLAUDE_CODE_USE_VERTEX is not set to "1".
// Returns an error if Vertex is enabled but misconfigured.
func ResolveVertex() (*VertexConfig, error) {
	if os.Getenv("CLAUDE_CODE_USE_VERTEX") != "1" {
		return nil, nil
	}

	region := os.Getenv("CLOUD_ML_REGION")
	if region == "" {
		return nil, fmt.Errorf("CLAUDE_CODE_USE_VERTEX=1 but CLOUD_ML_REGION is not set")
	}

	projectID := os.Getenv("ANTHROPIC_VERTEX_PROJECT_ID")
	if projectID == "" {
		return nil, fmt.Errorf("CLAUDE_CODE_USE_VERTEX=1 but ANTHROPIC_VERTEX_PROJECT_ID is not set")
	}

	token, err := resolveGCPToken()
	if err != nil {
		return nil, fmt.Errorf("Vertex AI enabled but cannot get GCP token: %w\n\nRun: gcloud auth login", err)
	}

	return &VertexConfig{
		Region:    region,
		ProjectID: projectID,
		Token:     token,
	}, nil
}

func resolveGCPToken() (string, error) {
	cmd := exec.Command("gcloud", "auth", "print-access-token")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gcloud auth print-access-token failed: %w", err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", fmt.Errorf("gcloud returned empty token")
	}
	return token, nil
}
