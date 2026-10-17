package nodeagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The test executable stands in for wg; no real kernel or network is changed.
func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "show" {
		switch os.Args[2] {
		case "wgargs":
			if len(os.Environ()) != 4 || os.Getenv("LC_ALL") != "C" || os.Getenv("WG_HIDE_KEYS") != "always" || os.Args[3] != "public-key" {
				os.Exit(9)
			}
			fmt.Print("fixed-args-clean-environment\n")
			os.Exit(0)
		case "wgbig":
			fmt.Print(strings.Repeat("x", 262145))
			os.Exit(0)
		case "wgfail":
			fmt.Fprint(os.Stderr, "sensitive diagnostic")
			os.Exit(4)
		case "wgslow":
			time.Sleep(10 * time.Second)
			os.Exit(0)
		}
		os.Exit(8)
	}
	os.Exit(m.Run())
}
func TestWGCommandBoundedReadOnlyAndNoSecretOutput(t *testing.T) {
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	c := &WGCommand{Binary: binary}
	t.Setenv("WG_TEST_SECRET", "must-not-pass")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	raw, e := c.Show(context.Background(), "wgargs", "public-key")
	if e != nil || string(raw) != "fixed-args-clean-environment\n" {
		t.Fatal("argv/environment isolation failed", e)
	}
	for _, args := range [][2]string{{"all", "public-key"}, {"interfaces", "public-key"}, {"wg0;id", "public-key"}, {"wg0", "dump"}, {"wg0", "private-key"}, {"wg0", "preshared-keys"}, {"wg0", "showconf"}, {"wg0", "set"}, {"wgbig", "public-key"}, {"wgfail", "public-key"}} {
		b, e := c.Show(context.Background(), args[0], args[1])
		if e == nil || len(b) != 0 || strings.Contains(e.Error(), "sensitive") {
			t.Fatal("unsafe command or output escaped", args, e)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, e = c.Show(ctx, "wgslow", "public-key"); e == nil || time.Since(start) > time.Second {
		t.Fatal("cancel did not bound process", e)
	}
	path := filepath.Join(t.TempDir(), "wg-link")
	if e = os.Symlink(binary, path); e != nil {
		t.Fatal(e)
	}
	c.Binary = path
	if _, e = c.Show(context.Background(), "wgargs", "public-key"); e == nil {
		t.Fatal("symlink binary accepted")
	}
	path = filepath.Join(t.TempDir(), "wg")
	if e = os.WriteFile(path, []byte("not an executable"), 0777); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(path, 0777); e != nil {
		t.Fatal(e)
	}
	c.Binary = path
	if _, e = c.Show(context.Background(), "wgargs", "public-key"); e == nil {
		t.Fatal("writable binary accepted")
	}
}
