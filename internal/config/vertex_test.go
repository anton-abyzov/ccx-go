package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveVertex_Disabled(t *testing.T) {
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
	cfg, err := ResolveVertex()
	assert.NoError(t, err)
	assert.Nil(t, cfg)
}

func TestResolveVertex_DisabledZero(t *testing.T) {
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "0")
	cfg, err := ResolveVertex()
	assert.NoError(t, err)
	assert.Nil(t, cfg)
}

func TestResolveVertex_MissingRegion(t *testing.T) {
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "1")
	t.Setenv("CLOUD_ML_REGION", "")
	t.Setenv("ANTHROPIC_VERTEX_PROJECT_ID", "my-project")

	_, err := ResolveVertex()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CLOUD_ML_REGION")
}

func TestResolveVertex_MissingProjectID(t *testing.T) {
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "1")
	t.Setenv("CLOUD_ML_REGION", "us-east5")
	t.Setenv("ANTHROPIC_VERTEX_PROJECT_ID", "")

	_, err := ResolveVertex()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ANTHROPIC_VERTEX_PROJECT_ID")
}
