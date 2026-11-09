// Package zfs wraps the zfs commands dbtwin uses.
package zfs

import (
	"fmt"
	"strings"

	"github.com/princerafid01/dbtwin/internal/command"
)

// Snapshot makes a read-only snapshot, e.g. tank/mysql-main@pr-1.
func Snapshot(snap string) error {
	return command.Run("zfs", "snapshot", snap)
}

// Clone turns a snapshot into a new writable dataset.
func Clone(snap, dataset string) error {
	return command.Run("zfs", "clone", snap, dataset)
}

// Destroy deletes a dataset or a snapshot.
func Destroy(name string) error {
	return command.Run("zfs", "destroy", name)
}

// Exists reports whether a dataset exists.
func Exists(dataset string) bool {
	return command.Succeeds("zfs", "list", "-H", dataset)
}

// SnapshotExists reports whether a snapshot exists.
func SnapshotExists(snap string) bool {
	return command.Succeeds("zfs", "list", "-H", "-t", "snapshot", snap)
}

// Mountpoint asks ZFS where a dataset is mounted, e.g. /tank/mysql-pr-1.
// We ask instead of guessing, because the mountpoint can be changed.
func Mountpoint(dataset string) (string, error) {
	mp, err := command.Output("zfs", "get", "-H", "-o", "value", "mountpoint", dataset)
	if err != nil {
		return "", err
	}
	// these values mean the dataset has no usable folder path
	if mp == "" || mp == "-" || mp == "none" || mp == "legacy" {
		return "", fmt.Errorf("dataset %s has no usable mountpoint (got %q)", dataset, mp)
	}
	return mp, nil
}

// Dataset is one line of "zfs list": a dataset name and its disk usage.
type Dataset struct {
	Name string // e.g. tank/mysql-pr-1
	Used string // e.g. 1.2M
}

// List returns every dataset in the pool with its disk usage.
func List(pool string) ([]Dataset, error) {
	// -H = no header, -o name,used = only these two columns, -r = all datasets in the pool
	out, err := command.Output("zfs", "list", "-H", "-o", "name,used", "-r", pool)
	if err != nil {
		return nil, err
	}

	var datasets []Dataset
	// go through each line, e.g. "tank/mysql-pr-1	1.2M"
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line) // split into [name, used]
		if len(fields) != 2 {
			continue
		}
		datasets = append(datasets, Dataset{Name: fields[0], Used: fields[1]})
	}
	return datasets, nil
}
