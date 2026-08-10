package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAIProfileMaxOutputTokens(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		value   int64
		wantErr bool
	}{
		{name: "unset", value: 0},
		{name: "minimum", value: 1},
		{name: "above former limit", value: 1 << 40},
		{name: "negative", value: -1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			profile := AIProfile{Name: "text", Kind: AIProfileText, BaseURL: "https://provider.example/v1", Model: "model", APIKey: "secret", MaxOutputTokens: tt.value, ContextWindowChars: 10000, TimeoutSec: 60, Enabled: true}
			err := profile.Validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "max_output_tokens")
				return
			}
			require.NoError(t, err)
		})
	}
}
