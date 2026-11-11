// Package config loads dbtwin's settings from defaults, a config file
// and environment variables.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// configPath is where dbtwin looks for its optional config file.
const configPath = "/etc/dbtwin/config.json"

// Config holds every setting dbtwin needs.
// The json tags are the key names used in the config file.
type Config struct {
	Pool             string `json:"pool"`               // ZFS pool that holds everything
	MainDataset      string `json:"main_dataset"`       // dataset with the main MySQL data
	BranchPrefix     string `json:"branch_prefix"`      // branch datasets are BranchPrefix + name
	ContainerPrefix  string `json:"container_prefix"`   // branch containers are ContainerPrefix + name
	MySQLImage       string `json:"mysql_image"`        // Docker image for branch containers
	BindAddress      string `json:"bind_address"`       // address branch ports listen on
	BranchMemory     string `json:"branch_memory"`      // memory limit for each branch container, e.g. 512m
	BranchBufferPool string `json:"branch_buffer_pool"` // InnoDB buffer pool size for each branch, e.g. 128M

	// the source MySQL server that "dbtwin refresh" copies from.
	// these may be empty; only "refresh" needs them.
	SourceHost      string `json:"source_host"`      // host of the source MySQL server
	SourcePort      string `json:"source_port"`      // port of the source MySQL server
	SourceUser      string `json:"source_user"`      // MySQL user to read the source with
	SourcePassword  string `json:"source_password"`  // password for SourceUser, never printed
	SourceDatabases string `json:"source_databases"` // comma-separated list, e.g. "shop,blog"
}

// defaultConfig returns the built-in settings.
func defaultConfig() Config {
	return Config{
		Pool:             "tank",
		MainDataset:      "tank/mysql-main",
		BranchPrefix:     "tank/mysql-",
		ContainerPrefix:  "mysql-",
		MySQLImage:       "mysql:8.4",
		BindAddress:      "127.0.0.1",
		BranchMemory:     "512m",
		BranchBufferPool: "128M",
		SourcePort:       "3306",
	}
}

// Load builds the settings in three layers:
// 1. built-in defaults
// 2. the config file, if it exists (only the keys it contains)
// 3. DBTWIN_* environment variables, if they are set and not empty
// It also reports whether the config file was found.
func Load() (Config, bool, error) {
	cfg := defaultConfig()

	// layer 2: the config file
	fileUsed := false
	data, err := os.ReadFile(configPath)
	switch {
	case err == nil:
		fileUsed = true
		// decoding into cfg only changes the keys that are in the file;
		// every other field keeps its default
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields() // catch typos like "mysql_imgae"
		if err := dec.Decode(&cfg); err != nil {
			return cfg, fileUsed, fmt.Errorf("config file %s is invalid: %v", configPath, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// no config file is fine, keep the defaults
	default:
		// the file exists but can't be read (e.g. no permission)
		return cfg, fileUsed, fmt.Errorf("can't read config file %s: %v", configPath, err)
	}

	// layer 3: environment variables
	envs := []struct {
		name  string
		field *string
	}{
		{"DBTWIN_POOL", &cfg.Pool},
		{"DBTWIN_MAIN_DATASET", &cfg.MainDataset},
		{"DBTWIN_BRANCH_PREFIX", &cfg.BranchPrefix},
		{"DBTWIN_CONTAINER_PREFIX", &cfg.ContainerPrefix},
		{"DBTWIN_MYSQL_IMAGE", &cfg.MySQLImage},
		{"DBTWIN_BIND_ADDRESS", &cfg.BindAddress},
		{"DBTWIN_BRANCH_MEMORY", &cfg.BranchMemory},
		{"DBTWIN_BRANCH_BUFFER_POOL", &cfg.BranchBufferPool},
		{"DBTWIN_SOURCE_HOST", &cfg.SourceHost},
		{"DBTWIN_SOURCE_PORT", &cfg.SourcePort},
		{"DBTWIN_SOURCE_USER", &cfg.SourceUser},
		{"DBTWIN_SOURCE_PASSWORD", &cfg.SourcePassword},
		{"DBTWIN_SOURCE_DATABASES", &cfg.SourceDatabases},
	}
	for _, e := range envs {
		if v := os.Getenv(e.name); v != "" {
			*e.field = v
		}
	}

	// an empty setting would make zfs/docker commands fail in confusing ways
	if err := cfg.check(); err != nil {
		return cfg, fileUsed, err
	}
	return cfg, fileUsed, nil
}

// check makes sure no setting is empty.
// The source_* settings are not checked here: they may be empty,
// and "refresh" checks them itself.
func (c Config) check() error {
	fields := map[string]string{
		"pool":               c.Pool,
		"main_dataset":       c.MainDataset,
		"branch_prefix":      c.BranchPrefix,
		"container_prefix":   c.ContainerPrefix,
		"mysql_image":        c.MySQLImage,
		"bind_address":       c.BindAddress,
		"branch_memory":      c.BranchMemory,
		"branch_buffer_pool": c.BranchBufferPool,
	}
	for key, value := range fields {
		if value == "" {
			return fmt.Errorf("config setting %q is empty", key)
		}
	}
	return nil
}

// Print shows the settings in use, as JSON.
// The output can be saved as a config file.
// The source password is shown as "***", so it never ends up on screen.
func Print(cfg Config, fileUsed bool) error {
	// cfg is a copy, so this doesn't change the password the caller uses
	if cfg.SourcePassword != "" {
		cfg.SourcePassword = "***"
	}

	if fileUsed {
		fmt.Fprintln(os.Stderr, "config file:", configPath)
	} else {
		fmt.Fprintln(os.Stderr, "config file:", configPath, "(not found, using defaults)")
	}

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}
