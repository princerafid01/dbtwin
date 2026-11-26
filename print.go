package main

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/princerafid01/dbtwin/internal/branch"
)

// printReady tells the user how to connect to a new or reset branch.
func printReady(b branch.Info) {
	fmt.Printf("branch %s is ready\n", b.Name)
	fmt.Printf("  host:     %s\n", b.Host)
	fmt.Printf("  port:     %d\n", b.Port)
	fmt.Printf("  user:     root (same users and passwords as main)\n")
	fmt.Printf("  connect:  mysql -h %s -P %d -u root -p\n", b.Host, b.Port)
}

// printList prints main and every branch with its disk usage, port and data date.
func printList(infos []branch.Info) {
	fmt.Printf("%-12s %-8s %-16s %s\n", "BRANCH", "USED", "PORT", "DATA FROM")
	for _, b := range infos {
		name := b.Name
		if b.Main {
			name = "(main)"
		}

		// e.g. 127.0.0.1:41235, or "not running" if MySQL is stopped
		port := "not running"
		if b.Status == branch.Running {
			port = net.JoinHostPort(b.Host, strconv.Itoa(b.Port))
		}
		fmt.Printf("%-12s %-8s %-16s %s\n", name, b.Used, port, formatDataDate(b.DataFrom))
	}
}

// formatDataDate shows a data date as a short local time like 2026-11-23 03:00.
// No date shows as "unknown".
func formatDataDate(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Local().Format("2006-01-02 15:04")
}
