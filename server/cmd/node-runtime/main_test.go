package main

import (
	"context"
	"testing"
)

func TestNodeRuntimeDefaultOffAndInvalidArguments(t *testing.T) {
	t.Setenv("NODE_WG_OBSERVER_ENABLED", "false")
	if e := run(context.Background(), nil); e != nil {
		t.Fatal(e)
	}
	if e := run(context.Background(), []string{"apply"}); e == nil {
		t.Fatal("write command accepted")
	}
	t.Setenv("NODE_WG_OBSERVER_ENABLED", "invalid")
	if e := run(context.Background(), nil); e == nil {
		t.Fatal("invalid gate accepted")
	}
}
