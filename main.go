package main

import (
	"fmt"
	"os"

	"github.com/princerafid01/dbtwin/internal/branch"
	"github.com/princerafid01/dbtwin/internal/config"
)

// main reads the command and calls the right function.
// Usage: dbtwin create <name> | dbtwin delete <name> | dbtwin list | dbtwin config
func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: dbtwin <create|delete|list|config> [name]")
		os.Exit(1)
	}
	command := os.Args[1]

	// load settings before doing anything, so a bad config stops us early
	cfg, fileUsed, err := config.Load()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	switch command {
	case "list":
		err = branch.List(cfg)
	case "config":
		err = config.Print(cfg, fileUsed)
	case "create", "delete":
		if len(os.Args) < 3 {
			fmt.Println("usage: dbtwin", command, "<name>")
			os.Exit(1)
		}
		name := os.Args[2] // not "branch", which is the package name
		if command == "create" {
			err = branch.Create(cfg, name)
		} else {
			err = branch.Delete(cfg, name)
		}
	default:
		fmt.Println("unknown command:", command)
		os.Exit(1)
	}

	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
