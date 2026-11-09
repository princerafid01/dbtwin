package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath" // joining folder paths safely
	"regexp"
	"strconv"
	"strings"
	"time"
)

// run executes an outside command (like "zfs snapshot ...").
// It prints the command first, then runs it.
// If the command fails, it returns an error that includes the command's output.
func run(name string, args ...string) error {
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

// freePort asks the operating system for an unused port on addr.
// Listening on port 0 means "give me any free port".
// We read which port we got, then close it so MySQL can use it.
func freePort(addr string) (int, error) {
	l, err := net.Listen("tcp", net.JoinHostPort(addr, "0"))
	if err != nil {
		return 0, err
	}
	defer l.Close() // release the port when this function ends
	return l.Addr().(*net.TCPAddr).Port, nil
}

// validName allows only letters, numbers, dash and underscore, 1 to 40 characters.
// This keeps names safe for zfs, docker and the shell.
var validName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

// checkName stops if the branch name has unsafe characters or is too long.
func checkName(branch string) error {
	if !validName.MatchString(branch) {
		return fmt.Errorf("invalid branch name %q: use only letters, numbers, - and _ (max 40 characters)", branch)
	}
	return nil
}

// checkNotMain stops if the branch would use the main dataset,
// e.g. branch "main" with the defaults gives tank/mysql-main.
// Without this, "dbtwin delete main" would remove the main MySQL container.
func checkNotMain(cfg Config, branch string) error {
	if cfg.BranchPrefix+branch == cfg.MainDataset {
		return fmt.Errorf("invalid branch name %q: it would use the main dataset %s", branch, cfg.MainDataset)
	}
	return nil
}

// exists runs a command and reports whether it succeeded.
// We use it to ask zfs or docker "does this thing exist?"
func exists(name string, args ...string) bool {
	return exec.Command(name, args...).Run() == nil
}

// lastLogs returns the last 20 lines the container printed.
// We grab them before cleanup, because cleanup deletes the container and its logs.
// MySQL logs to stderr, so we keep both stdout and stderr.
func lastLogs(container string) string {
	out, _ := exec.Command("docker", "logs", "--tail", "20", container).CombinedOutput()
	return strings.TrimSpace(string(out))
}

// waitForMySQL checks every second until MySQL in the container answers,
// or gives up after timeout. It stops early if the container has stopped,
// because a crashed MySQL will never answer.
// mysqladmin ping succeeds as soon as the server responds, even without a password.
func waitForMySQL(container string, timeout time.Duration) error {
	fmt.Println("waiting for MySQL...")
	deadline := time.Now().Add(timeout)
	for {
		if exists("docker", "exec", container, "mysqladmin", "ping", "--silent") {
			return nil
		}

		// is the container still running? "true" or "false"
		running, err := output("docker", "inspect", "-f", "{{.State.Running}}", container)
		if err != nil || running != "true" {
			return fmt.Errorf("MySQL in %s stopped while starting. last log lines:\n%s", container, lastLogs(container))
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("MySQL in %s was not ready after %v. last log lines:\n%s", container, timeout, lastLogs(container))
		}
		time.Sleep(time.Second)
	}
}

// checkBranchFree stops if any part of the branch already exists.
// It runs before we change anything, so there is nothing to clean up.
func checkBranchFree(branch, snap, clone, container string) error {
	if exists("zfs", "list", "-H", clone) ||
		exists("zfs", "list", "-H", "-t", "snapshot", snap) ||
		exists("docker", "inspect", container) {
		return fmt.Errorf("branch %s already exists (use \"dbtwin delete %s\" first)", branch, branch)
	}
	return nil
}

// createBranch makes a new branch:
// snapshot main -> clone -> delete auto.cnf -> start MySQL
func createBranch(cfg Config, branch string) (err error) {
	snap := cfg.MainDataset + "@" + branch    // e.g. tank/mysql-main@pr-1
	clone := cfg.BranchPrefix + branch        // e.g. tank/mysql-pr-1
	container := cfg.ContainerPrefix + branch // e.g. mysql-pr-1

	// stop early if the name is unsafe or the branch already exists
	if err := checkName(branch); err != nil {
		return err
	}
	if err := checkNotMain(cfg, branch); err != nil {
		return err
	}
	if err := checkBranchFree(branch, snap, clone, container); err != nil {
		return err
	}

	// step 1: snapshot the main database
	if err := run("zfs", "snapshot", snap); err != nil {
		return err
	}

	// if ANY later step fails, undo everything this function made.
	// defer runs when the function ends; err is set if a step failed.
	defer func() {
		if err != nil {
			fmt.Println("create failed, cleaning up...")
			run("docker", "rm", "-f", container) // errors ignored on purpose
			run("zfs", "destroy", clone)
			run("zfs", "destroy", snap)
		}
	}()

	// step 2: clone the snapshot into a writable branch
	if err := run("zfs", "clone", snap, clone); err != nil {
		return err
	}

	// step 3: find where the clone is mounted
	dir, err := mountpoint(clone)
	if err != nil {
		return err
	}

	// delete auto.cnf so the branch gets its own MySQL server ID
	if err := os.Remove(filepath.Join(dir, "auto.cnf")); err != nil {
		return fmt.Errorf("remove auto.cnf failed: %v", err)
	}

	// step 4: find a free port for this branch
	port, err := freePort(cfg.BindAddress)
	if err != nil {
		return err
	}
	// e.g. 127.0.0.1:41235 (JoinHostPort adds [ ] around IPv6 addresses)
	hostPort := net.JoinHostPort(cfg.BindAddress, strconv.Itoa(port))

	// step 5: start MySQL on the clone, using that port
	if err := run("docker", "run", "-d",
		"--name", container,
		"-v", dir+":/var/lib/mysql", // real clone folder -> MySQL data folder
		"-p", hostPort+":3306", // e.g. 127.0.0.1:41235:3306
		cfg.MySQLImage,
	); err != nil {
		return err
	}

	// step 6: wait until MySQL really accepts connections.
	// if it never does, returning the error runs the cleanup above.
	if err := waitForMySQL(container, 60*time.Second); err != nil {
		return err
	}

	// tell the user how to connect
	fmt.Printf("branch %s is ready\n", branch)
	fmt.Printf("  host:     %s\n", cfg.BindAddress)
	fmt.Printf("  port:     %d\n", port)
	fmt.Printf("  user:     root (same users and passwords as main)\n")
	fmt.Printf("  connect:  mysql -h %s -P %d -u root -p\n", cfg.BindAddress, port)
	return nil
}

// deleteBranch removes a branch in the safe order:
// stop MySQL -> delete clone -> delete snapshot
func deleteBranch(cfg Config, branch string) error {
	if err := checkName(branch); err != nil {
		return err
	}
	if err := checkNotMain(cfg, branch); err != nil {
		return err
	}

	snap := cfg.MainDataset + "@" + branch
	clone := cfg.BranchPrefix + branch

	// step 1: stop and remove the branch's MySQL container
	if err := run("docker", "rm", "-f", cfg.ContainerPrefix+branch); err != nil {
		return err
	}

	// step 2: delete the clone (must happen before the snapshot)
	if err := run("zfs", "destroy", clone); err != nil {
		return err
	}

	// step 3: delete the snapshot it was made from
	return run("zfs", "destroy", snap)
}

// output runs a command and returns what it printed, as clean text.
// Use this when we need the result (run() only checks success).
func output(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return "", fmt.Errorf("%s failed: %v", name, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// mountpoint asks ZFS where a dataset is mounted, e.g. /tank/mysql-pr-1.
// We ask instead of guessing, because the mountpoint can be changed.
func mountpoint(dataset string) (string, error) {
	mp, err := output("zfs", "get", "-H", "-o", "value", "mountpoint", dataset)
	if err != nil {
		return "", err
	}
	// these values mean the dataset has no usable folder path
	if mp == "" || mp == "-" || mp == "none" || mp == "legacy" {
		return "", fmt.Errorf("dataset %s has no usable mountpoint (got %q)", dataset, mp)
	}
	return mp, nil
}

// listBranches prints every branch with its disk usage and port.
func listBranches(cfg Config) error {
	// -H = no header, -o name,used = only these two columns, -r = all datasets in the pool
	out, err := output("zfs", "list", "-H", "-o", "name,used", "-r", cfg.Pool)
	if err != nil {
		return err
	}

	fmt.Printf("%-12s %-8s %s\n", "BRANCH", "USED", "PORT")

	// go through each line, e.g. "tank/mysql-pr-1	1.2M"
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line) // split into [name, used]
		if len(fields) != 2 {
			continue
		}
		name, used := fields[0], fields[1]

		// keep only branch datasets, skip main and others
		if !strings.HasPrefix(name, cfg.BranchPrefix) || name == cfg.MainDataset {
			continue
		}
		branch := strings.TrimPrefix(name, cfg.BranchPrefix) // "pr-1"

		// ask Docker which port this branch's MySQL uses
		port, err := output("docker", "port", cfg.ContainerPrefix+branch, "3306")
		if err != nil {
			port = "not running"
		}

		fmt.Printf("%-12s %-8s %s\n", branch, used, port)
	}
	return nil
}

// main reads the command and calls the right function.
// Usage: dbtwin create <name> | dbtwin delete <name> | dbtwin list | dbtwin config
func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: dbtwin <create|delete|list|config> [name]")
		os.Exit(1)
	}
	command := os.Args[1]

	// load settings before doing anything, so a bad config stops us early
	cfg, fileUsed, err := loadConfig()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	switch command {
	case "list":
		err = listBranches(cfg)
	case "config":
		err = printConfig(cfg, fileUsed)
	case "create", "delete":
		if len(os.Args) < 3 {
			fmt.Println("usage: dbtwin", command, "<name>")
			os.Exit(1)
		}
		branch := os.Args[2]
		if command == "create" {
			err = createBranch(cfg, branch)
		} else {
			err = deleteBranch(cfg, branch)
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
