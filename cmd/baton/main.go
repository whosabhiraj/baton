// Command baton is the Baton CLI: what agents call to report to, and hear from, the hub.
package main

import (
	"os"

	"github.com/whosabhiraj/baton/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}
