// Command whatsagent is a bridge between a WhatsApp account and Claude Code and Codex sessions.
package main

import (
	"os"

	"github.com/osolmaz/WhatsAgent/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
