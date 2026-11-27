package main

import (
	"fmt"
	"os"

	"github.com/princerafid01/dbtwin/internal/api"
	"github.com/princerafid01/dbtwin/internal/branch"
	"github.com/princerafid01/dbtwin/internal/check"
	"github.com/princerafid01/dbtwin/internal/config"
	"github.com/princerafid01/dbtwin/internal/lock"
	"github.com/princerafid01/dbtwin/internal/refresh"
	"github.com/princerafid01/dbtwin/internal/source"
)

// version is the dbtwin version. It is "dev" for local builds and is set
// at release time with: go build -ldflags "-X main.version=v1.0.0"
var version = "dev"

// main reads the command and calls the right function.
// Usage: dbtwin create <name> | dbtwin delete <name> | dbtwin reset <name> | dbtwin list |
// dbtwin connect --host <host> --user <user> --databases <databases> |
// dbtwin connect --test | dbtwin refresh | dbtwin serve | dbtwin config | dbtwin version
func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: dbtwin <create|delete|reset|list|refresh|connect|serve|config|version> [name]")
		os.Exit(1)
	}
	command := os.Args[1]

	// "version" needs nothing else, so answer it right away
	if command == "version" {
		fmt.Println("dbtwin", version)
		return
	}

	// CHECK THE ARGUMENTS
	// first make sure the command is valid, so a typo gets
	// "unknown command" or the usage line, not a startup check error
	name := ""                   // not "branch", which is the package name
	var connectArgs connectFlags // the --host, --port... values, only used by "connect"

	switch command {
	case "list", "config", "refresh", "serve":
	case "create", "delete", "reset":
		if len(os.Args) < 3 {
			fmt.Println("usage: dbtwin", command, "<name>")
			os.Exit(1)
		}
		name = os.Args[2]
	case "connect":
		// stops with the usage line if --host, --user or --databases is missing,
		// or if --test is mixed with other flags
		connectArgs = parseConnectFlags(os.Args[2:])
	default:
		fmt.Println("unknown command:", command)
		os.Exit(1)
	}

	// LOAD config
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

	// only one command that changes branches or main runs at a time.
	// os.Exit below skips the deferred release, which is fine: the kernel
	// drops the lock as soon as dbtwin ends, however it ends.
	// the defer is still needed: it keeps the lock file open. without it,
	// Go's garbage collector could close the file mid-command and drop the lock.
	if command == "create" || command == "delete" || command == "reset" || command == "refresh" {
		release, err := lock.Take()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		defer release()
	}

	switch command {
	case "list":
		var infos []branch.Info
		if infos, err = branch.List(cfg); err == nil {
			printList(infos)
		}
	case "config":
		err = config.Print(cfg, fileUsed)
	case "create":
		var info branch.Info
		if info, err = branch.Create(cfg, name); err == nil {
			printReady(info)
		}
	case "delete":
		err = branch.Delete(cfg, name)
	case "reset":
		var info branch.Info
		if info, err = branch.Reset(cfg, name); err == nil {
			printReady(info)
		}
	case "refresh":
		err = refresh.Run(cfg)
	case "serve":
		// runs until stopped; each change takes the lock itself
		err = api.Run(cfg)
	case "connect":
		if connectArgs.test {
			// re-test the saved settings; changes nothing
			_, err = source.PrintCheck(cfg)
		} else {
			err = runConnect(cfg, connectArgs)
		}
	}

	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
