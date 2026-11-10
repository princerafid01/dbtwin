// Package docker wraps the docker commands dbtwin uses.
package docker

import (
	"fmt"
	"time"

	"github.com/princerafid01/dbtwin/internal/command"
)

// RunMySQL starts a MySQL container on a data folder,
// with MySQL's port 3306 published on hostPort (e.g. 127.0.0.1:41235).
// memory caps the container's RAM (e.g. 512m) and bufferPool sets
// MySQL's InnoDB buffer pool size (e.g. 128M), so it fits inside that cap.
func RunMySQL(container, dataDir, hostPort, image, memory, bufferPool string) error {
	return command.Run("docker", "run", "-d",
		"--restart", "unless-stopped", // come back after a Docker or server restart
		"--name", container,
		"-v", dataDir+":/var/lib/mysql", // real clone folder -> MySQL data folder
		"-p", hostPort+":3306", // e.g. 127.0.0.1:41235:3306
		"--memory", memory, // e.g. 512m, so one branch can't use all the server's RAM
		image,
		// everything after the image name is passed to mysqld
		"--innodb-buffer-pool-size="+bufferPool, // e.g. 128M, must fit inside --memory
		"--performance-schema=OFF",              // saves extra RAM
	)
}

// Remove stops and deletes a container.
func Remove(container string) error {
	return command.Run("docker", "rm", "-f", container)
}

// Exists reports whether a container exists (running or not).
func Exists(container string) bool {
	return command.Succeeds("docker", "inspect", container)
}

// Port returns the host address of the container's MySQL port,
// e.g. 127.0.0.1:41235.
func Port(container string) (string, error) {
	return command.Output("docker", "port", container, "3306")
}

// lastLogs returns the last 20 lines the container printed.
// We grab them before cleanup, because cleanup deletes the container and its logs.
// MySQL logs to stderr, so we keep both stdout and stderr.
func lastLogs(container string) string {
	return command.CombinedOutput("docker", "logs", "--tail", "20", container)
}

// WaitForMySQL checks every second until MySQL in the container answers,
// or gives up after timeout. It stops early if the container has stopped,
// because a crashed MySQL will never answer.
// mysqladmin ping succeeds as soon as the server responds, even without a password.
func WaitForMySQL(container string, timeout time.Duration) error {
	fmt.Println("waiting for MySQL...")
	deadline := time.Now().Add(timeout)
	for {
		if command.Succeeds("docker", "exec", container, "mysqladmin", "ping", "--silent") {
			return nil
		}

		// is the container still running, and has it never crashed? e.g. "running 0".
		// with --restart, Docker brings a crashed MySQL back up on its own,
		// so a restart count above 0 also means it crashed.
		state, err := command.Output("docker", "inspect", "-f", "{{.State.Status}} {{.RestartCount}}", container)
		if err != nil || state != "running 0" {
			return fmt.Errorf("MySQL in %s stopped while starting. last log lines:\n%s", container, lastLogs(container))
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("MySQL in %s was not ready after %v. last log lines:\n%s", container, timeout, lastLogs(container))
		}
		time.Sleep(time.Second)
	}
}
