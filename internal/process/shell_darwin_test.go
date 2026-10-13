//go:build darwin

package process

import (
	"reflect"
	"testing"
)

func TestDarwinScriptArgumentOrder(t *testing.T) {
	cmd, err := shellCommand("echo hello world", true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"script", "-q", "-e", "/dev/null", "/bin/bash", "-lc", "echo hello world"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("got %q, want %q", cmd.Args, want)
	}
}
