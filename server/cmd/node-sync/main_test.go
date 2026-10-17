package main

import (
	"context"
	"testing"
)

func TestNodeSyncDefaultOffAndArgumentGuard(t *testing.T) {
	t.Setenv("NODE_WG_SYNC_ENABLED", "false")
	if e := run(context.Background(), nil); e != nil {
		t.Fatal(e)
	}
	if e := run(context.Background(), []string{"apply"}); e == nil {
		t.Fatal("write operation accepted")
	}
	t.Setenv("NODE_WG_SYNC_ENABLED", "TRUE")
	if e := run(context.Background(), nil); e == nil {
		t.Fatal("loose gate")
	}
}
