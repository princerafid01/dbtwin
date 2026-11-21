// Package refresh copies the source databases into main.
//
// The order is:
// (main.go has already locked dbtwin)
// check the source and main -> safety snapshot ->
// drop the old databases -> dump and load -> remove the safety snapshot.
// If any step after the safety snapshot fails, main is rolled back to it.
package refresh

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/princerafid01/dbtwin/internal/config"
	"github.com/princerafid01/dbtwin/internal/docker"
	"github.com/princerafid01/dbtwin/internal/source"
	"github.com/princerafid01/dbtwin/internal/zfs"
)

// safetyName is the safety snapshot, e.g. tank/mysql-main@before-refresh.
// It only exists while a refresh is running.
func safetyName(cfg config.Config) string {
	return cfg.MainDataset + "@before-refresh"
}

// errNotBuilt is returned at the end of Run until every step is built,
// so a refresh that copied nothing never reports success.
var errNotBuilt = errors.New("refresh is not finished yet: the data was not copied")

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
			fmt.Println("refresh failed, rolling main back...")
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
	if err := dumpAndLoad(cfg); err != nil {
		return err
	}
	loaded = true

	// FINISH
	if err := removeSafety(cfg); err != nil {
		return err
	}
	return errNotBuilt
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

// dumpAndLoad streams mysqldump from the source straight into main,
// showing progress, and checks the dump really finished.
func dumpAndLoad(cfg config.Config) error { return nil }

// removeSafety deletes main@before-refresh after a good load.
func removeSafety(cfg config.Config) error { return nil }
