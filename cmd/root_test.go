package cmd

import (
	"strings"
	"testing"
)

func TestServeFlagBinding(t *testing.T) {
	root := NewRootCmd()
	root.SetArgs([]string{"serve", "--request-rate", "0"})
	if err := root.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "request rate") {
		t.Fatalf("Execute() error=%v, want request rate validation", err)
	}
}
