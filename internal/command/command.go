// Package command runs outside programs like zfs and docker.
// It is shared by the zfs and docker packages, so these helpers live in one place.
package command

import (
	"bytes"
	"fmt"
	"os"
	"os/exec" // running other programs like zfs and docker
	"strings"
)

// Run executes an outside command (like "zfs snapshot ...").
// It prints the command first, then runs it.
// If the command fails, it returns an error that includes the command's output.
func Run(name string, args ...string) error {
	// show which command is about to run
	fmt.Println(">", name, args)

	// run the command and capture everything it prints (output + errors)
	out, err := exec.Command(name, args...).CombinedOutput()

	// if the command failed, return a helpful error message
	if err != nil {
		return fmt.Errorf("%s failed: %v\n%s", name, err, out)
	}

	// no error means the command succeeded
	return nil
}

// Output runs a command and returns what it printed, as clean text.
// Use this when we need the result (Run() only checks success).
func Output(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return "", fmt.Errorf("%s failed: %v", name, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Succeeds runs a command and reports whether it succeeded.
// We use it to ask zfs or docker "does this thing exist?"
func Succeeds(name string, args ...string) bool {
	return exec.Command(name, args...).Run() == nil
}

// CombinedOutput runs a command and returns everything it printed
// (stdout and stderr), ignoring whether it failed.
func CombinedOutput(name string, args ...string) string {
	out, _ := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out))
}

// OutputEnv runs a command with extra environment variables (like
// "MYSQL_PWD=secret") and returns what it printed to stdout and stderr,
// separately and as clean text.
// Use this to pass secrets: unlike arguments, environment variables
// don't show up in "ps" or in the "> command" line Run() prints.
// Nothing is printed before the command runs.
func OutputEnv(env []string, name string, args ...string) (string, string, error) {
	cmd := exec.Command(name, args...)
	// keep the normal environment (PATH, DOCKER_HOST, ...) and add ours;
	// if a name is in both, the later one (ours) wins
	cmd.Env = append(os.Environ(), env...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}
