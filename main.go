package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath" // joining folder paths safely
	"strings"
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

// freePort asks the operating system for an unused port.
// Listening on port 0 means "give me any free port".
// We read which port we got, then close it so MySQL can use it.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close() // release the port when this function ends
	return l.Addr().(*net.TCPAddr).Port, nil
}

// createBranch makes a new branch:
// snapshot main -> clone -> delete auto.cnf -> start MySQL
func createBranch(branch string) (err error) {
	snap := "tank/mysql-main@" + branch // e.g. tank/mysql-main@pr-1
	clone := "tank/mysql-" + branch     // e.g. tank/mysql-pr-1

	// step 1: snapshot the main database
	if err := run("zfs", "snapshot", snap); err != nil {
		return err
	}

	// if ANY later step fails, undo everything this function made.
	// defer runs when the function ends; err is set if a step failed.
	defer func() {
		if err != nil {
			fmt.Println("create failed, cleaning up...")
			run("docker", "rm", "-f", "mysql-"+branch) // errors ignored on purpose
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
	port, err := freePort()
	if err != nil {
		return err
	}

	// step 5: start MySQL on the clone, using that port
	if err := run("docker", "run", "-d",
		"--name", "mysql-"+branch,
		"-v", dir+":/var/lib/mysql", // real clone folder -> MySQL data folder
		"-p", fmt.Sprintf("127.0.0.1:%d:3306", port), // e.g. 127.0.0.1:41235:3306
		"mysql:8.4",
	); err != nil {
		return err
	}

	// tell the user how to connect
	fmt.Printf("branch %s ready on 127.0.0.1:%d\n", branch, port)
	return nil
}


// deleteBranch removes a branch in the safe order:
// stop MySQL -> delete clone -> delete snapshot
func deleteBranch(branch string) error {
	snap := "tank/mysql-main@" + branch
	clone := "tank/mysql-" + branch

	// step 1: stop and remove the branch's MySQL container
	if err := run("docker", "rm", "-f", "mysql-"+branch); err != nil {
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
func listBranches() error {
	// -H = no header, -o name,used = only these two columns, -r tank = all datasets
	out, err := output("zfs", "list", "-H", "-o", "name,used", "-r", "tank")
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
		if !strings.HasPrefix(name, "tank/mysql-") || name == "tank/mysql-main" {
			continue
		}
		branch := strings.TrimPrefix(name, "tank/mysql-") // "pr-1"

		// ask Docker which port this branch's MySQL uses
		port, err := output("docker", "port", "mysql-"+branch, "3306")
		if err != nil {
			port = "not running"
		}

		fmt.Printf("%-12s %-8s %s\n", branch, used, port)
	}
	return nil
}


// main reads the command and calls the right function.
// Usage: dbtwin create <name> | dbtwin delete <name> | dbtwin list
func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: dbtwin <create|delete|list> [name]")
		os.Exit(1)
	}
	command := os.Args[1]

	var err error
	switch command {
	case "list":
		err = listBranches()
	case "create", "delete":
		if len(os.Args) < 3 {
			fmt.Println("usage: dbtwin", command, "<name>")
			os.Exit(1)
		}
		branch := os.Args[2]
		if command == "create" {
			err = createBranch(branch)
		} else {
			err = deleteBranch(branch)
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
