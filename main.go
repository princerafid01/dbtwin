package main

import (
	"fmt"
	"os"

	"github.com/princerafid01/dbtwin/internal/branch"
	"github.com/princerafid01/dbtwin/internal/check"
	"github.com/princerafid01/dbtwin/internal/config"
	"github.com/princerafid01/dbtwin/internal/source"
)

// version is the dbtwin version. It is "dev" for local builds and is set
// at release time with: go build -ldflags "-X main.version=v1.0.0"
var version = "dev"

// main reads the command and calls the right function.
// Usage: dbtwin create <name> | dbtwin delete <name> | dbtwin list |
// dbtwin refresh --check | dbtwin config | dbtwin version
func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: dbtwin <create|delete|list|refresh|config|version> [name]")
		os.Exit(1)
	}
	command := os.Args[1]

	// "version" needs nothing else, so answer it right away
	if command == "version" {
		fmt.Println("dbtwin", version)
		return
	}

	// first make sure the command is valid, so a typo gets
	// "unknown command" or the usage line, not a startup check error
	name := "" // not "branch", which is the package name
	switch command {
	case "list", "config":
	case "create", "delete":
		if len(os.Args) < 3 {
			fmt.Println("usage: dbtwin", command, "<name>")
			os.Exit(1)
		}
		name = os.Args[2]
	case "refresh":
		// only the connection check exists so far, copying data comes later
		if len(os.Args) < 3 || os.Args[2] != "--check" {
			fmt.Println("usage: dbtwin refresh --check (copying data is not supported yet)")
			os.Exit(1)
		}
	default:
		fmt.Println("unknown command:", command)
		os.Exit(1)
	}

	// load settings before doing anything, so a bad config stops us early
	cfg, fileUsed, err := config.Load()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	// make sure zfs, docker, root and main are ready
	// (skip for commands that change nothing, like "config")
	if command != "config" {
		if err := check.All(cfg.MainDataset); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
	}

	switch command {
	case "list":
		err = branch.List(cfg)
	case "config":
		err = config.Print(cfg, fileUsed)
	case "create":
		err = branch.Create(cfg, name)
	case "delete":
		err = branch.Delete(cfg, name)
	case "refresh":
		err = source.PrintCheck(cfg)
	}

	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
