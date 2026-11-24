// Package branch creates, deletes and lists database branches,
// using zfs for the data and docker for the MySQL servers.
package branch

import (
	"fmt"
	"net"
	"os"
	"path/filepath" // joining folder paths safely
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/princerafid01/dbtwin/internal/config"
	"github.com/princerafid01/dbtwin/internal/docker"
	"github.com/princerafid01/dbtwin/internal/zfs"
)

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

// portFree reports whether port is unused on addr.
// We try to listen on it: if that works, nobody else has it.
func portFree(addr string, port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	l.Close()
	return true
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
func checkNotMain(cfg config.Config, branch string) error {
	if cfg.BranchPrefix+branch == cfg.MainDataset {
		return fmt.Errorf("invalid branch name %q: it would use the main dataset %s", branch, cfg.MainDataset)
	}
	return nil
}

// checkBranchFree stops if any part of the branch already exists.
// It runs before we change anything, so there is nothing to clean up.
func checkBranchFree(branch, snap, clone, container string) error {
	if zfs.Exists(clone) ||
		zfs.SnapshotExists(snap) ||
		docker.Exists(container) {
		return fmt.Errorf("branch %s already exists (use \"dbtwin delete %s\" first)", branch, branch)
	}
	return nil
}

// Create makes a new branch on any free port.
func Create(cfg config.Config, branch string) error {
	return create(cfg, branch, 0)
}

// create makes a new branch:
// snapshot main -> clone -> delete auto.cnf -> start MySQL
// port is the host port for the branch's MySQL; 0 means pick a free one.
func create(cfg config.Config, branch string, port int) (err error) {
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

	// step 1: snapshot the main database, copying main's data date onto it,
	// so this branch keeps its own date when main is refreshed later
	date, err := zfs.LocalProperty(cfg.MainDataset, zfs.DataFrom)
	if err != nil {
		fmt.Printf("warning: could not read main's data date: %v\n", err)
		date = ""
	}
	if err := zfs.SnapshotWithProperty(snap, zfs.DataFrom, date); err != nil {
		return err
	}

	// if ANY later step fails, undo everything this function made.
	// defer runs when the function ends; err is set if a step failed.
	defer func() {
		if err != nil {
			fmt.Println("create failed, cleaning up...")
			docker.Remove(container) // errors ignored on purpose
			zfs.Destroy(clone)
			zfs.Destroy(snap)
		}
	}()

	// step 2: clone the snapshot into a writable branch
	if err := zfs.Clone(snap, clone); err != nil {
		return err
	}

	// step 3: find where the clone is mounted
	dir, err := zfs.Mountpoint(clone)
	if err != nil {
		return err
	}

	// delete auto.cnf so the branch gets its own MySQL server ID
	if err := os.Remove(filepath.Join(dir, "auto.cnf")); err != nil {
		return fmt.Errorf("remove auto.cnf failed: %v", err)
	}

	// step 4: use the given port if it is still free, else find a free one
	if port != 0 && !portFree(cfg.BindAddress, port) {
		fmt.Printf("warning: port %d is now used by something else, using a new port\n", port)
		port = 0
	}
	if port == 0 {
		port, err = freePort(cfg.BindAddress)
		if err != nil {
			return err
		}
	}
	// e.g. 127.0.0.1:41235 (JoinHostPort adds [ ] around IPv6 addresses)
	hostPort := net.JoinHostPort(cfg.BindAddress, strconv.Itoa(port))

	// step 5: start MySQL on the clone, using that port
	if err := docker.RunMySQL(container, dir, hostPort, cfg.MySQLImage, cfg.BranchMemory, cfg.BranchBufferPool); err != nil {
		return err
	}

	// step 6: wait until MySQL really accepts connections.
	// if it never does, returning the error runs the cleanup above.
	if err := docker.WaitForMySQL(container, 60*time.Second); err != nil {
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

// Delete removes a branch in the safe order:
// stop MySQL -> delete clone -> delete snapshot
func Delete(cfg config.Config, branch string) error {
	if err := checkName(branch); err != nil {
		return err
	}
	if err := checkNotMain(cfg, branch); err != nil {
		return err
	}

	snap := cfg.MainDataset + "@" + branch
	clone := cfg.BranchPrefix + branch

	// step 1: stop and remove the branch's MySQL container
	if err := docker.Remove(cfg.ContainerPrefix + branch); err != nil {
		return err
	}

	// step 2: delete the clone (must happen before the snapshot)
	if err := zfs.Destroy(clone); err != nil {
		return err
	}

	// step 3: delete the snapshot it was made from
	return zfs.Destroy(snap)
}

// formatDataDate turns the saved date (e.g. 2026-11-23T03:00:00+06:00)
// into a short local time like 2026-11-23 03:00.
// No date, or one we can't read, shows as "unknown".
func formatDataDate(raw string) string {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return "unknown"
	}
	return t.Local().Format("2006-01-02 15:04")
}

// List prints main and every branch with its disk usage, port and data date.
func List(cfg config.Config) error {
	datasets, err := zfs.List(cfg.Pool)
	if err != nil {
		return err
	}

	fmt.Printf("%-12s %-8s %-16s %s\n", "BRANCH", "USED", "PORT", "DATA FROM")

	// main first, so branches can be compared to it
	for _, d := range datasets {
		if d.Name == cfg.MainDataset {
			date, _ := zfs.LocalProperty(cfg.MainDataset, zfs.DataFrom)

			port, err := docker.Port(cfg.MainContainer)
			if err != nil {
				port = "not running"
			}
			fmt.Printf("%-12s %-8s %-16s %s\n", "(main)", d.Used, port, formatDataDate(date))
		}
	}

	for _, d := range datasets {
		// keep only branch datasets, skip main and others
		if !strings.HasPrefix(d.Name, cfg.BranchPrefix) || d.Name == cfg.MainDataset {
			continue
		}
		branch := strings.TrimPrefix(d.Name, cfg.BranchPrefix) // "pr-1"

		// ask Docker which port this branch's MySQL uses
		port, err := docker.Port(cfg.ContainerPrefix + branch)
		if err != nil {
			port = "not running"
		}

		// the date lives on the branch's own snapshot, e.g. tank/mysql-main@pr-1
		date, _ := zfs.LocalProperty(cfg.MainDataset+"@"+branch, zfs.DataFrom)

		fmt.Printf("%-12s %-8s %-16s %s\n", branch, d.Used, port, formatDataDate(date))
	}
	return nil
}

// Reset gives a branch main's newest data, keeping its name and port.
// Everything changed on the branch is lost.
// remember the port -> delete the branch -> create it again on that port
func Reset(cfg config.Config, branchName string) error {
	if err := checkName(branchName); err != nil {
		return err
	}
	if err := checkNotMain(cfg, branchName); err != nil {
		return err
	}

	// the branch must exist, or there is nothing to reset
	clone := cfg.BranchPrefix + branchName
	container := cfg.ContainerPrefix + branchName
	if !zfs.Exists(clone) || !docker.Exists(container) {
		return fmt.Errorf("branch %s not found (see \"dbtwin list\")", branchName)
	}

	// step 1: remember the port, so apps using this branch keep working
	port, err := docker.HostPort(container)
	if err != nil {
		return err
	}

	// step 2: delete the branch and all its changes
	if err := Delete(cfg, branchName); err != nil {
		return err
	}

	// step 3: make it again from main, on the same port
	if err := create(cfg, branchName, port); err != nil {
		return fmt.Errorf("%v\nbranch %s was deleted but could not be made again. Run: dbtwin create %s", err, branchName, branchName)
	}
	return nil
}
