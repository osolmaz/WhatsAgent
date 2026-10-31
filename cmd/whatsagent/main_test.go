package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestBinary runs main in a child process, because main ends the process with os.Exit.
func TestBinary(t *testing.T) {
	if os.Getenv("WHATSAGENT_TEST_RUN_MAIN") == "1" {
		os.Args = []string{"whatsagent", "version"}
		main()
		return
	}
	child := exec.Command(os.Args[0], "-test.run=^TestBinary$")
	child.Env = append(os.Environ(), "WHATSAGENT_TEST_RUN_MAIN=1")
	out, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("whatsagent version: %v, output %q", err, out)
	}
	if strings.TrimSpace(string(out)) != "dev" {
		t.Fatalf("whatsagent version printed %q", out)
	}
}
