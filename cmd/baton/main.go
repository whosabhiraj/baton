// Command baton is the Baton CLI: what agents call to report to, and hear from, the hub.
package main

import (
	"os"

	"github.com/whosabhiraj/baton/internal/cli"
	"github.com/whosabhiraj/baton/internal/conductor"
)

func main() {
	if os.Getenv(conductor.FakeEnv) == "1" { // `baton mock`'s stand-in for the conductor CLI
		os.Exit(conductor.FakeCLI(os.Args[1:], os.Stdin, os.Stdout))
	}
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}
