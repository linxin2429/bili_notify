package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServeFlagBinding(t *testing.T) {
	t.Parallel()
	root := NewRootCmd()
	root.SetArgs([]string{"serve", "--request-rate", "0"})
	err := root.ExecuteContext(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "request rate")
}
