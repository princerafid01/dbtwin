// Package refresh copies the source databases into main.
//
// The order is:
// (main.go has already locked dbtwin)
// check the source and main -> safety snapshot ->
// drop the old databases -> dump and load -> remove the safety snapshot.
// If any step after the safety snapshot fails, or the user presses Ctrl+C,
// main is rolled back to it.
package refresh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/princerafid01/dbtwin/internal/config"
	"github.com/princerafid01/dbtwin/internal/docker"
	"github.com/princerafid01/dbtwin/internal/source"
	"github.com/princerafid01/dbtwin/internal/zfs"
)

// progress passes data through unchanged and prints how much has gone by.
type progress struct {
	r       io.Reader // where the data comes from (mysqldump's output)
	total   int64     // bytes passed so far
	printed int64     // total at the last print
	tail    []byte    // the last 256 bytes that passed, to check how the dump ended
}

// Read is called by io.Copy in dumpAndLoad, once per chunk.
func (p *progress) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf) // 1. get data from mysqldump
	p.total += int64(n)

	// remember the last 256 bytes, to check later how the dump ended
	p.tail = append(p.tail, buf[:n]...)
	if len(p.tail) > 256 {
		p.tail = p.tail[len(p.tail)-256:]
	}

	if p.total-p.printed >= 10*1024*1024 {
		fmt.Printf("\rcopied %d MB…", p.total>>20)
		p.printed = p.total
	}
	return n, err
}

// safetyName is the safety snapshot, e.g. tank/mysql-main@before-refresh.
// It only exists while a refresh is running.
func safetyName(cfg config.Config) string {
	return cfg.MainDataset + "@before-refresh"
}

// errStopped is returned when the user stops the refresh (Ctrl+C).
var errStopped = errors.New("refresh was stopped (Ctrl+C)")

// Run copies the source databases into main.
func Run(cfg config.Config) (err error) {
	// BEFORE STARTING
	// these checks change nothing, so a problem stops us with nothing to undo

	// the saved source settings must still connect
	dbs, err := source.PrintCheck(cfg)
	if err != nil {
		return err
	}
	// the main container must be running, and we must be able to log in as root
	if err := checkMain(cfg); err != nil {
		return err
	}
	// the pool needs room for the new copy of the data
	if err := checkFreeSpace(cfg, dbs); err != nil {
		return err
	}

	// a leftover safety snapshot means an earlier refresh crashed half-way
	if err := checkNoLeftover(cfg); err != nil {
		return err
	}

	// CATCH Ctrl+C
	// before this point, Ctrl+C just ends dbtwin: nothing was changed yet.
	// from here on, Ctrl+C (or "kill") must not end dbtwin at once, or main
	// would stay half-loaded. instead it cancels ctx: the running step
	// stops, it returns an error, and the rollback below runs.
	// this defer is set up before the rollback's, so it runs after it:
	// Ctrl+C stays caught until the rollback is done.
	ctx, stopCatching := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopCatching()
	// stopped returns errStopped once the user has pressed Ctrl+C
	stopped := func() error {
		if ctx.Err() != nil {
			return errStopped
		}
		return nil
	}

	// SAFETY NET
	if err := safetySnapshot(cfg); err != nil {
		return err
	}
	// from here on, if any step fails, put main back the way it was.
	// defer runs when Run ends; err is set if a step failed.
	// loaded is set once the new data is fully in main: after that a
	// rollback would throw good data away, so a later error must not do it.
	loaded := false
	defer func() {
		if err != nil && !loaded {
			fmt.Println()
			fmt.Println("refresh failed, rolling main back. Please wait, don't stop dbtwin now...")
			if rbErr := rollback(cfg); rbErr != nil {
				err = fmt.Errorf("%v\nrollback ALSO failed: %v", err, rbErr)
			}
		}
	}()

	// COPY THE DATA
	// drop first, so tables removed on the source don't stay in main
	if err := dropDatabases(cfg); err != nil {
		return err
	}
	if err := stopped(); err != nil {
		return err
	}
	if err := dumpAndLoad(ctx, cfg); err != nil {
		return err
	}
	if err := stopped(); err != nil {
		return err
	}
	// from here on the new data is good; Ctrl+C no longer undoes it
	loaded = true

	// FINISH
	if err := removeSafety(cfg); err != nil {
		return err
	}
	fmt.Printf("main now has a fresh copy of: %s\n", strings.Join(source.Databases(cfg), ", "))
	return nil
}

// checkMain makes sure refresh will load into the right MySQL:
// 1. the main container is running
// 2. it really keeps its data in the main dataset
// 3. we can log in to it as root
func checkMain(cfg config.Config) error {
	name := cfg.MainContainer

	// 1. is it running?
	running, err := docker.Running(name)
	if err != nil {
		return fmt.Errorf("main container %s not found. Start it (see the README Quick start) or set main_container", name)
	}
	if !running {
		return fmt.Errorf("main container %s is not running. Start it with: docker start %s", name, name)
	}

	// 2. is it using the main dataset? if main_container points at some
	// other MySQL, refresh would drop databases in the wrong server
	want, err := zfs.Mountpoint(cfg.MainDataset)
	if err != nil {
		return err
	}
	got, err := docker.MountSource(name, "/var/lib/mysql")
	if err != nil {
		return err
	}
	if filepath.Clean(got) != filepath.Clean(want) {
		if got == "" {
			got = "(nothing mounted)"
		}
		return fmt.Errorf("main container %s keeps its data in %s, not in %s (%s). Check main_container and main_dataset",
			name, got, cfg.MainDataset, want)
	}

	// 3. can we log in as root?
	if err := docker.RootLogin(name); err != nil {
		return fmt.Errorf("can't log in to main (%s) as root using $MYSQL_ROOT_PASSWORD.\n"+
			"If root's password was changed, recreate the container with the new password.\n%v", name, err)
	}
	return nil
}

// checkFreeSpace stops if main has less free space than the source data.
// The old data stays on disk (in the safety snapshot) while the new data
// is loaded, so the new copy needs room of its own.
// The source size is a rough guess: ZFS compression usually makes the
// copy smaller, so this errs on the safe side.
func checkFreeSpace(cfg config.Config, dbs []source.Database) error {
	needMB := 0.0
	for _, db := range dbs {
		needMB += db.SizeMB
	}

	free, err := zfs.Available(cfg.MainDataset)
	if err != nil {
		return err
	}
	freeMB := float64(free) / 1024 / 1024

	if freeMB < needMB {
		return fmt.Errorf("not enough disk space: the source data is about %.0f MB, but %s has only %.0f MB free. "+
			"Free some space (e.g. \"dbtwin delete\" old branches) and try again", needMB, cfg.MainDataset, freeMB)
	}
	return nil
}

// checkNoLeftover stops if the safety snapshot already exists.
// It only exists while a refresh runs, and we hold the lock, so no other
// refresh is running: an earlier one must have died half-way (power loss,
// kill -9) without rolling back, and main may be half-loaded.
// We don't fix it ourselves: only the user knows if main is fine or not.
func checkNoLeftover(cfg config.Config) error {
	snap := safetyName(cfg)
	if !zfs.SnapshotExists(snap) {
		return nil
	}
	c := cfg.MainContainer
	return fmt.Errorf("%s already exists: an earlier refresh did not finish, so main may be half-loaded.\n"+
		"To put main back the way it was before that refresh:\n"+
		"  docker stop %s\n"+
		"  zfs rollback %s\n"+
		"  docker start %s\n"+
		"  zfs destroy %s\n"+
		"If you are sure main is fine, only delete the snapshot:\n"+
		"  zfs destroy %s\n"+
		"Then run \"dbtwin refresh\" again.", snap, c, snap, c, snap, snap)
}

// The steps below are not built yet. Each one does nothing and returns nil.

// safetySnapshot snapshots main as main@before-refresh, so a failed
// refresh can put main back exactly as it is now.
// MySQL keeps running: the snapshot is like the state after a sudden power
// cut, and InnoDB repairs itself from that when it starts after a rollback.
func safetySnapshot(cfg config.Config) error {
	return zfs.Snapshot(safetyName(cfg))
}

// rollback puts main back exactly as it was before the refresh:
// stop MySQL -> zfs rollback -> start MySQL -> wait until it answers ->
// delete the safety snapshot.
//
// MySQL must be stopped first: it has the data files open, and changing
// them under a running MySQL would corrupt main.
// If any step fails, the safety snapshot is kept, so the next refresh
// stops in checkNoLeftover and tells the user how to finish by hand.
func rollback(cfg config.Config) error {
	if err := docker.Stop(cfg.MainContainer); err != nil {
		return err
	}
	if err := zfs.Rollback(safetyName(cfg)); err != nil {
		return err
	}
	if err := docker.Start(cfg.MainContainer); err != nil {
		return err
	}
	if err := docker.WaitForMySQL(cfg.MainContainer, 120*time.Second); err != nil {
		return err
	}
	// main is back as it was; the snapshot isn't needed any more
	if err := zfs.Destroy(safetyName(cfg)); err != nil {
		return err
	}
	fmt.Println("main was rolled back; it is the same as before the refresh")
	return nil
}

// dropDatabases drops each source database in main, so the load starts
// from nothing: a table that was dropped on the source must not stay in main.
// Databases in main that are not in source_databases are left alone.
// Names were already checked by the source check (plain characters only,
// no MySQL system databases), so they are safe inside backticks.
func dropDatabases(cfg config.Config) error {
	names := source.Databases(cfg)
	sql := ""
	for _, name := range names {
		sql += "DROP DATABASE IF EXISTS `" + name + "`; "
	}
	fmt.Println("dropping the old copies in main:", strings.Join(names, ", "))
	if err := docker.RootSQL(cfg.MainContainer, sql); err != nil {
		return fmt.Errorf("can't drop the old databases in main: %v", err)
	}
	return nil
}

// removeSafety deletes main@before-refresh after a good load.
// If this fails, the new data is still good: we only tell the user
// how to delete the backup, so the next refresh is not blocked.
func removeSafety(cfg config.Config) error {
	snap := safetyName(cfg)
	if err := zfs.Destroy(snap); err != nil {
		return fmt.Errorf("the new data is loaded, but the backup %s could not be deleted: %v\n"+
			"Delete it with: zfs destroy %s", snap, err, snap)
	}
	return nil
}

// dumpContainer is the name of the throwaway mysqldump container,
// e.g. dbtwin-dump-4121 (with dbtwin's process id, so it is unique).
func dumpContainer() string {
	return fmt.Sprintf("dbtwin-dump-%d", os.Getpid())
}

// stopDump stops mysqldump for good. Killing the "docker run" command is
// not enough: the container it started keeps running, and would keep
// reading from the source. So the container is removed too.
// Errors are ignored on purpose: it may already be gone.
func stopDump(dump *exec.Cmd) {
	dump.Process.Kill()
	exec.Command("docker", "rm", "-f", dumpContainer()).Run()
}

// dumpCmd builds the mysqldump command that reads the source databases.
// It runs in a throwaway container, so the host needs no mysqldump.
// The password goes in MYSQL_PWD, so it never shows up in "ps".
func dumpCmd(cfg config.Config) *exec.Cmd {
	args := []string{"run", "--rm",
		"--name", dumpContainer(), // so stopDump can remove it by name
		"--log-driver", "none", // never store the dump in Docker's logs
		"--network", "host", // so 127.0.0.1 means this machine, not the container
		"-e", "MYSQL_PWD", // the value comes from cmd.Env below
		cfg.MySQLImage,
		"mysqldump",
		"--protocol", "TCP",
		"-h", cfg.SourceHost,
		"-P", cfg.SourcePort,
		"-u", cfg.SourceUser,
		"--single-transaction",  // consistent copy, no locks on production (InnoDB)
		"--quick",               // send rows one by one, don't hold tables in memory
		"--routines",            // include stored procedures and functions
		"--triggers",            // include triggers
		"--events",              // include scheduled events
		"--no-tablespaces",      // avoids needing the PROCESS privilege
		"--set-gtid-purged=OFF", // a GTID line would make the load fail in main
		"--databases",           // adds CREATE DATABASE and USE for each one
	}
	for _, db := range source.Databases(cfg) {
		args = append(args, db)
	}

	cmd := exec.Command("docker", args...)
	// always set it, even when empty, so a MYSQL_PWD in the user's shell is never sent
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+cfg.SourcePassword)
	return cmd
}

// loadCmd builds the mysql command that loads SQL from stdin into main.
// It runs inside main's container and uses the container's own root
// password, so dbtwin never sees it.
func loadCmd(cfg config.Config) *exec.Cmd {
	script := `MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot`
	return exec.Command("docker", "exec", "-i", cfg.MainContainer, "sh", "-c", script)
}

// dumpAndLoad streams mysqldump from the source straight into main,
// showing progress. Both commands must succeed.
// When ctx is cancelled (Ctrl+C), both commands are killed.
func dumpAndLoad(ctx context.Context, cfg config.Config) error {
	dump := dumpCmd(cfg)
	load := loadCmd(cfg)

	fromDump, err := dump.StdoutPipe() // mysqldump's output comes out here
	if err != nil {
		return err
	}
	toLoad, err := load.StdinPipe() // mysql's input goes in here
	if err != nil {
		return err
	}
	p := &progress{r: fromDump} // counts everything read from mysqldump

	// keep each command's error messages, to show them if it fails
	var dumpErr, loadErr bytes.Buffer
	dump.Stderr = &dumpErr
	load.Stderr = &loadErr

	if err = dump.Start(); err != nil {
		return fmt.Errorf("can't start mysqldump: %v", err)
	}

	if err := load.Start(); err != nil {
		stopDump(dump)
		dump.Wait()
		return fmt.Errorf("can't start mysql in main: %v", err)
	}

	// on Ctrl+C, kill both: the copy below then ends with an error
	stopWatching := context.AfterFunc(ctx, func() {
		stopDump(dump)
		load.Process.Kill()
	})
	defer stopWatching()

	// THE COPY: io.Copy loops by itself, chunk by chunk:
	// read a chunk from p (mysqldump, counted) -> write it to mysql -> repeat,
	// until mysqldump has nothing left, or mysql stops taking data
	_, copyErr := io.Copy(toLoad, p)
	toLoad.Close() // tell mysql: "no more data coming"

	// if mysql stopped early, mysqldump has nobody to write to: stop it
	if copyErr != nil {
		stopDump(dump)
	}

	// wait until both have really exited, and get how each one ended.
	// the copy is already over, so the order doesn't matter here
	loadResponse := load.Wait()
	dumpResponse := dump.Wait()
	fmt.Printf("\rcopied %d MB\n", p.total>>20) // final total, on its own line

	// stopped by the user: say so, instead of the "killed" errors that follow
	if ctx.Err() != nil {
		return errStopped
	}

	// mysql in main failed: the load is incomplete, so Run will roll back
	if loadResponse != nil {
		return loadError(loadResponse, loadErr.String(), dumpErr.String())
	}

	// mysql succeeded, but that alone proves nothing: if mysqldump died
	// half-way, mysql just sees the data end and exits OK with half of it
	// loaded. so mysqldump must have succeeded too
	if dumpResponse != nil {
		return dumpError(dumpResponse, dumpErr.String())
	}

	// both succeeded, but a dump can still stop early without an error.
	// a complete dump always ends with "-- Dump completed", so check for it
	if !bytes.Contains(p.tail, []byte("-- Dump completed")) {
		return errors.New("the dump ended early: its last line \"-- Dump completed\" never arrived")
	}
	return nil
}

// loadError explains why loading into main failed, with what mysql printed.
// A dump that broke half-way can make mysql fail with a confusing syntax
// error, so what mysqldump printed is added too, if it printed anything.
func loadError(res error, loadMsg, dumpMsg string) error {
	msg := fmt.Sprintf("loading into main failed: %v\n%s", res, loadMsg)
	if dumpMsg != "" {
		msg += "\nmysqldump said:\n" + dumpMsg
	}
	return errors.New(msg)
}

// dumpError explains why mysqldump failed, with what it printed.
func dumpError(res error, dumpMsg string) error {
	return fmt.Errorf("mysqldump from source failed: %v\n%s", res, dumpMsg)
}
