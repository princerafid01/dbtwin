// Package source talks to the source MySQL server that "dbtwin refresh"
// copies from. For now it only checks the connection and the databases.
package source

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/princerafid01/dbtwin/internal/command"
	"github.com/princerafid01/dbtwin/internal/config"
)

// Database is one source database and how big it is.
type Database struct {
	Name   string
	SizeMB float64 // data + indexes, from information_schema.tables
}

// validDatabase allows only letters, numbers, _, $ and -, 1 to 64 characters.
// Database names go into a SQL query, so this keeps the query safe.
var validDatabase = regexp.MustCompile(`^[A-Za-z0-9_$-]{1,64}$`)

// validPort allows only a port number, e.g. 3306.
var validPort = regexp.MustCompile(`^[0-9]{1,5}$`)

// Databases splits source_databases ("shop, blog") into a list,
// trimming spaces and skipping empty entries.
func Databases(cfg config.Config) []string {
	var list []string
	for _, name := range strings.Split(cfg.SourceDatabases, ",") {
		if name = strings.TrimSpace(name); name != "" {
			list = append(list, name)
		}
	}
	return list
}

// checkSettings stops if a setting refresh needs is missing or unsafe.
// They may be empty in general, because only refresh uses them.
func checkSettings(cfg config.Config) error {
	missing := []struct {
		key, env, value string
	}{
		{"source_host", "DBTWIN_SOURCE_HOST", cfg.SourceHost},
		{"source_port", "DBTWIN_SOURCE_PORT", cfg.SourcePort},
		{"source_user", "DBTWIN_SOURCE_USER", cfg.SourceUser},
		{"source_databases", "DBTWIN_SOURCE_DATABASES", strings.Join(Databases(cfg), ",")},
	}
	for _, m := range missing {
		if m.value == "" {
			return fmt.Errorf("setting %q is empty. Set it in the config file or with %s.", m.key, m.env)
		}
	}

	if !validPort.MatchString(cfg.SourcePort) {
		return fmt.Errorf("source_port %q is not a port number", cfg.SourcePort)
	}
	for _, name := range Databases(cfg) {
		if !validDatabase.MatchString(name) {
			return fmt.Errorf("invalid database name %q in source_databases: use only letters, numbers, _, $ and - (max 64 characters)", name)
		}
	}
	return nil
}

// query runs SQL on the source server and returns the result rows,
// with the columns split on tabs.
// It uses a throwaway container, so the host needs no mysql client:
//
//	docker run --rm --network host -e MYSQL_PWD <image> mysql -h <host> -P <port> -u <user> -e "<sql>"
//
// The password is never on a command line. "-e MYSQL_PWD" without a value
// tells docker to copy MYSQL_PWD from its own environment, which we set.
func query(cfg config.Config, sql string) ([][]string, error) {
	args := []string{"run", "--rm",
		"--network", "host", // so 127.0.0.1 means this machine, not the container
		"-e", "MYSQL_PWD", // the value comes from our environment, see below
		cfg.MySQLImage,
		"mysql",
		"--protocol", "TCP", // "localhost" would otherwise try a socket inside the container
		"--connect-timeout", "10", // don't hang for minutes on a wrong host
		"-h", cfg.SourceHost,
		"-P", cfg.SourcePort,
		"-u", cfg.SourceUser,
		"--batch", "--skip-column-names", // plain tab-separated rows, no table borders
		"-e", sql,
	}

	// always set MYSQL_PWD, even when empty, so a MYSQL_PWD the user
	// happens to have in their shell is never sent instead
	stdout, stderr, err := command.OutputEnv([]string{"MYSQL_PWD=" + cfg.SourcePassword}, "docker", args...)
	if err != nil {
		return nil, explain(cfg, stderr, err)
	}

	var rows [][]string
	for _, line := range strings.Split(stdout, "\n") {
		if line != "" {
			rows = append(rows, strings.Split(line, "\t"))
		}
	}
	return rows, nil
}

// explain turns a failed mysql run into a clear error.
// The mysql client starts its messages with the error code, e.g.
// "ERROR 1045 (28000): Access denied for user ...".
func explain(cfg config.Config, stderr string, err error) error {
	addr := cfg.SourceHost + ":" + cfg.SourcePort
	switch {
	case strings.Contains(stderr, "ERROR 1045"):
		return fmt.Errorf("source %s rejected user %q: wrong user or password (check source_user and source_password)\n%s", addr, cfg.SourceUser, stderr)
	case strings.Contains(stderr, "ERROR 2005"):
		return fmt.Errorf("cannot connect to source %s: unknown host (check source_host)\n%s", addr, stderr)
	case strings.Contains(stderr, "ERROR 2003"),
		strings.Contains(stderr, "ERROR 2002"),
		strings.Contains(stderr, "ERROR 2013"):
		return fmt.Errorf("cannot connect to source %s: is MySQL running there and reachable from this machine? (check source_host and source_port)\n%s", addr, stderr)
	case strings.Contains(stderr, "ERROR"):
		return fmt.Errorf("query on source %s failed:\n%s", addr, stderr)
	default:
		// not a mysql error, so docker itself failed (e.g. the image can't be pulled)
		return fmt.Errorf("docker run %s failed: %v\n%s", cfg.MySQLImage, err, stderr)
	}
}

// sizeSQL builds the query that lists each database with its size in MB.
// It starts from information_schema.schemata, so an empty database is still
// listed (with size 0), and a missing one is simply not in the result.
func sizeSQL(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = "'" + name + "'" // safe: checkSettings allowed only plain characters
	}
	return "SELECT s.schema_name, " +
		"ROUND(COALESCE(SUM(t.data_length + t.index_length), 0) / 1024 / 1024, 1) " +
		"FROM information_schema.schemata s " +
		"LEFT JOIN information_schema.tables t ON t.table_schema = s.schema_name " +
		"WHERE s.schema_name IN (" + strings.Join(quoted, ", ") + ") " +
		"GROUP BY s.schema_name"
}

// Check connects to the source server and makes sure every database in
// source_databases exists. It returns them in the order of the setting.
func Check(cfg config.Config) ([]Database, error) {
	if err := checkSettings(cfg); err != nil {
		return nil, err
	}
	names := Databases(cfg)

	rows, err := query(cfg, sizeSQL(names))
	if err != nil {
		return nil, err
	}

	// size of every database the server knows about, by name
	sizes := map[string]float64{}
	for _, row := range rows {
		if len(row) != 2 {
			return nil, fmt.Errorf("unexpected answer from source: %q", strings.Join(row, "\t"))
		}
		size, err := strconv.ParseFloat(row[1], 64)
		if err != nil {
			return nil, fmt.Errorf("unexpected size %q for database %s", row[1], row[0])
		}
		sizes[row[0]] = size
	}

	var dbs []Database
	for _, name := range names {
		size, ok := sizes[name]
		if !ok {
			// information_schema only shows databases the user may see
			return nil, fmt.Errorf("database %s not found on source %s:%s (or user %q has no access to it)",
				name, cfg.SourceHost, cfg.SourcePort, cfg.SourceUser)
		}
		dbs = append(dbs, Database{Name: name, SizeMB: size})
	}
	return dbs, nil
}

// PrintCheck runs Check and prints each database with its size.
func PrintCheck(cfg config.Config) error {
	dbs, err := Check(cfg)
	if err != nil {
		return err
	}

	fmt.Printf("connected to %s:%s as %s\n", cfg.SourceHost, cfg.SourcePort, cfg.SourceUser)
	fmt.Printf("%-24s %10s\n", "DATABASE", "SIZE (MB)")
	for _, db := range dbs {
		fmt.Printf("%-24s %10.1f\n", db.Name, db.SizeMB)
	}
	return nil
}
