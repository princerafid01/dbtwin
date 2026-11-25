// Package mask hides private data in main after a refresh,
// using the rules in the "mask" setting.
package mask

import (
	"fmt"
	"sort"
	"strings"

	"github.com/princerafid01/dbtwin/internal/config"
	"github.com/princerafid01/dbtwin/internal/docker"
)

// Run masks every table in the rules, with one UPDATE per table.
// With no rules it does nothing, so refresh works as before.
func Run(cfg config.Config) error {
	if len(cfg.Mask) == 0 {
		return nil
	}
	// same order every run, so the output is easy to compare
	tables := make([]string, 0, len(cfg.Mask))

	for table := range cfg.Mask {
		tables = append(tables, table)

	}
	sort.Strings(tables)

	for _, table := range tables {
		// e.g. UPDATE `shop`.`customers` SET `email` = ..., `phone` = NULL
		sql, err := tableSQL(table, cfg.Mask[table])
		if err != nil {
			return err
		}

		if err := docker.RootSQLStdin(cfg.MainContainer, "SET @k = "+quote(cfg.MaskSecret)+"; "+sql); err != nil {
			return fmt.Errorf("masking %s failed: %v", table, err)
		}
		fmt.Println("masking", table)
	}

	return nil

}

// Check makes sure every mask rule can be turned into SQL:
// tables written as database.table, and only known rules.
// It runs nothing, so it's safe to call before the refresh starts.
func Check(cfg config.Config) error {
	for table, columns := range cfg.Mask {
		if _, err := tableSQL(table, columns); err != nil {
			return err
		}
	}
	return nil
}

// tableSQL builds the UPDATE for one table, e.g. for "shop.customers"
// with {"email": "email", "phone": "null"}:
// UPDATE `shop`.`customers` SET `email` = CONCAT(...), `phone` = NULL
func tableSQL(table string, columns map[string]string) (string, error) {
	db, name, ok := strings.Cut(table, ".")
	if !ok || db == "" || name == "" {
		return "", fmt.Errorf("mask rule %q: the table must be written as database.table", table)
	}

	// same order every run, like the tables
	cols := make([]string, 0, len(columns))
	for col := range columns {
		cols = append(cols, col)
	}
	sort.Strings(cols)
	var sets []string

	for _, col := range cols {
		value, err := ruleSQL(col, columns[col])
		if err != nil {
			return "", fmt.Errorf("mask rule %s.%s: %v", table, col, err)
		}
		sets = append(sets, ident(col)+" = "+value)
	}
	return "UPDATE " + ident(db) + "." + ident(name) + " SET " + strings.Join(sets, ", "), nil
}

func ruleSQL(col, rule string) (string, error) {
	c := ident(col)
	switch {
	case rule == "hash":
		// 16 characters: fits most text columns and is still unique in practice
		return "LEFT(SHA2(CONCAT(@k, " + c + "), 256), 16)", nil
	case rule == "email":
		return "CONCAT(LEFT(SHA2(CONCAT(@k, " + c + "), 256), 16), '@example.com')", nil
	case rule == "null":
		return "NULL", nil
	case strings.HasPrefix(rule, "fixed:"):
		return quote(strings.TrimPrefix(rule, "fixed:")), nil
	}
	return "", fmt.Errorf("unknown rule %q (use hash, email, null or fixed:<text>)", rule)
}

func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `''`)
	return "'" + s + "'"
}

func ident(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
