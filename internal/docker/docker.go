// Package docker wraps the docker commands dbtwin uses.
package docker

import (
	"encoding/json"
	"fmt"
	"strconv"
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

// Stop stops a container but keeps it, so it can be started again.
// Stopping a container that is already stopped is fine.
func Stop(container string) error {
	return command.Run("docker", "stop", container)
}

// Start starts a stopped container again.
func Start(container string) error {
	return command.Run("docker", "start", container)
}

// Remove stops and deletes a container.
func Remove(container string) error {
	return command.Run("docker", "rm", "-f", container)
}

// Exists reports whether a container exists (running or not).
func Exists(container string) bool {
	return command.Succeeds("docker", "inspect", container)
}

// Running reports whether a container is running.
// It returns an error if the container doesn't exist.
func Running(container string) (bool, error) {
	out, err := command.Output("docker", "inspect", "-f", "{{.State.Running}}", container)
	if err != nil {
		return false, err
	}
	return out == "true", nil
}

// MountSource returns the host folder mounted at dest inside the
// container, e.g. /tank/mysql-main for /var/lib/mysql.
// It returns "" if nothing is mounted there.
func MountSource(container, dest string) (string, error) {
	format := `{{range .Mounts}}{{if eq .Destination "` + dest + `"}}{{.Source}}{{end}}{{end}}`
	return command.Output("docker", "inspect", "-f", format, container)
}

// RootSQL runs SQL in the container's MySQL as root, using the
// container's own $MYSQL_ROOT_PASSWORD. The password goes in MYSQL_PWD,
// not on the command line, so it never shows up in "ps".
// The SQL is handed to sh as "$1", so the shell never reads into it.
// On failure, the error includes what mysql printed.
func RootSQL(container, sql string) error {
	script := `test -n "$MYSQL_ROOT_PASSWORD" || { echo "MYSQL_ROOT_PASSWORD is not set in the container" >&2; exit 1; }
MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot -e "$1"`
	_, stderr, err := command.OutputEnv(nil, "docker", "exec", container, "sh", "-c", script, "sh", sql)
	if err != nil {
		return fmt.Errorf("%v: %s", err, stderr)
	}
	return nil
}

// RootLogin checks that we can log in to MySQL in the container as root.
func RootLogin(container string) error {
	return RootSQL(container, "SELECT 1")
}

// Port returns the host address of the container's MySQL port,
// e.g. 127.0.0.1:41235.
func Port(container string) (string, error) {
	return command.Output("docker", "port", container, "3306")
}

// HostPort returns the host port the container's MySQL port 3306 was
// published on, e.g. 41235. Unlike Port, it also works when the
// container is stopped: it reads the container's settings, not its
// live state.
func HostPort(container string) (int, error) {
	// e.g. {"3306/tcp":[{"HostIp":"127.0.0.1","HostPort":"41235"}]}
	out, err := command.Output("docker", "inspect", "-f", "{{json .HostConfig.PortBindings}}", container)
	if err != nil {
		return 0, err
	}

	var bindings map[string][]struct{ HostPort string }

	if err := json.Unmarshal([]byte(out), &bindings); err != nil {
		return 0, fmt.Errorf("can't read the ports of %s: %v", container, err)
	}

	mysql := bindings["3306/tcp"]
	if len(mysql) == 0 {
		return 0, fmt.Errorf("container %s has no host port for MySQL", container)
	}
	return strconv.Atoi(mysql[0].HostPort)
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

// RootSQLStdin is like RootSQL, but sends the SQL on stdin instead of
// the command line. Use it when the SQL holds a secret: command lines
// show up in "ps", stdin doesn't.
func RootSQLStdin(container, sql string) error {
	script := `test -n "$MYSQL_ROOT_PASSWORD" || { echo "MYSQL_ROOT_PASSWORD is not set in the container" >&2; exit 1; }
MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot`

	_, stderr, err := command.OutputStdin(sql, "docker", "exec", "-i", container, "sh", "-c", script)

	if err != nil {
		return fmt.Errorf("%v: %s", err, stderr)
	}
	return nil

}
