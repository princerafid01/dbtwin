// Package check makes sure everything dbtwin needs is ready
// before any command runs, and explains problems in plain words.
package check

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/princerafid01/dbtwin/internal/command"
)

// All runs every startup check and stops at the first problem.
func All(mainDataset string) error {
	if err := root(); err != nil {
		return err
	}
	if err := installed("zfs", "Install ZFS first (package zfsutils-linux)."); err != nil {
		return err
	}
	if err := installed("docker", "Install Docker first."); err != nil {
		return err
	}
	if err := dockerRunning(); err != nil {
		return err
	}
	return mainExists(mainDataset)
}

// root stops if dbtwin is not run as root.
// zfs and docker both need root to change things.
func root() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("dbtwin needs root. Run it with sudo.")
	}
	return nil
}

// installed stops if a program is not found on this machine.
func installed(program, hint string) error {
	if _, err := exec.LookPath(program); err != nil {
		return fmt.Errorf("%s not found. %s", program, hint)
	}
	return nil
}

// dockerRunning stops if Docker is installed but its service is not running.
func dockerRunning() error {
	if !command.Succeeds("docker", "info") {
		return fmt.Errorf("docker is installed but not running. Start it with: systemctl start docker")
	}
	return nil
}

// mainExists stops if the main dataset is missing.
// Every branch is cloned from it, so nothing works without it.
func mainExists(dataset string) error {
	if !command.Succeeds("zfs", "list", "-H", dataset) {
		return fmt.Errorf("main dataset %s not found. See the README Quick start.", dataset)
	}
	return nil
}
