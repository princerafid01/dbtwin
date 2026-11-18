package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/princerafid01/dbtwin/internal/config"
	"github.com/princerafid01/dbtwin/internal/source"
)

// connectFlags are the settings given to "dbtwin connect".
type connectFlags struct {
	host, port, user, databases string
}

// parseConnectFlags reads the "dbtwin connect" flags and stops with the
// usage line if a required one is missing.
func parseConnectFlags(args []string) connectFlags {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	var f connectFlags
	fs.StringVar(&f.host, "host", "", "host of the source MySQL server (required)")
	fs.StringVar(&f.port, "port", "3306", "port of the source MySQL server")
	fs.StringVar(&f.user, "user", "", "MySQL user to read the source with (required)")
	fs.StringVar(&f.databases, "databases", "", "comma-separated databases, e.g. shop,blog (required)")
	fs.Parse(args) // on an unknown flag this prints the error and the help, then exits

	if fs.NArg() > 0 || f.host == "" || f.user == "" || f.databases == "" {
		fmt.Println("usage: dbtwin connect --host <host> --user <user> --databases <db1,db2> [--port 3306]")
		os.Exit(1)
	}
	return f
}

// runConnect checks the given source settings and, only if the check
// passes, saves them in the config file.
func runConnect(cfg config.Config, f connectFlags) error {
	// the flags replace whatever source settings were loaded
	cfg.SourceHost = f.host
	cfg.SourcePort = f.port
	cfg.SourceUser = f.user
	cfg.SourceDatabases = f.databases

	password, err := sourcePassword()
	if err != nil {
		return err
	}
	cfg.SourcePassword = password

	// the same check as "refresh --check"; if it fails, nothing is saved
	if err := source.PrintCheck(cfg); err != nil {
		return err
	}
	if err := config.SaveSource(cfg); err != nil {
		return err
	}
	fmt.Println("source settings saved. Run \"dbtwin refresh --check\" to test them again later.")
	return nil
}

// sourcePassword returns DBTWIN_SOURCE_PASSWORD if it is set,
// otherwise it asks for the password without showing what is typed.
func sourcePassword() (string, error) {
	if p := os.Getenv("DBTWIN_SOURCE_PASSWORD"); p != "" {
		return p, nil
	}

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("can't ask for the password: not running in a terminal. Set DBTWIN_SOURCE_PASSWORD instead.")
	}

	fmt.Fprint(os.Stderr, "source password: ")
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr) // the Enter key isn't shown either, so end the line ourselves
	if err != nil {
		return "", fmt.Errorf("can't read the password: %v", err)
	}
	return string(b), nil
}
