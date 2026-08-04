package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestHashPasswordCommand(t *testing.T) {
	root := NewRootCmd()
	out := new(bytes.Buffer)
	root.SetOut(out)
	root.SetErr(new(bytes.Buffer))
	root.SetIn(strings.NewReader("correct horse battery staple\ncorrect horse battery staple\n"))
	root.SetArgs([]string{"admin", "hash-password"})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "$argon2id$") {
		t.Fatalf("output=%q", out.String())
	}
}

func TestServeFlagBinding(t *testing.T) {
	root := NewRootCmd()
	root.SetArgs([]string{"serve", "--request-rate", "0"})
	if err := root.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "request rate") {
		t.Fatalf("Execute() error=%v, want request rate validation", err)
	}
}
