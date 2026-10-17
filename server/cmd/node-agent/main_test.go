package main

import (
	"context"
	"testing"
)

func TestNodeAgentDefaultsAndArguments(t *testing.T) {
	t.Setenv("NODE_AGENT_ENABLED", "false")
	if err := run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"reset-state"}); err == nil {
		t.Fatal("reset command accepted")
	}
	t.Setenv("NODE_AGENT_ENABLED", "TRUE")
	if err := run(context.Background(), nil); err == nil {
		t.Fatal("invalid gate accepted")
	}
}
