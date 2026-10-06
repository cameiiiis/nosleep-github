package app

import (
	"database/sql"
	"fmt"
	"strings"
)

// Convert old databases once, retaining IDs, relationships and original quotes.
// The old column names appear only here for backwards compatibility.
func migratePesos(db *sql.DB, sqlText string) error {
	var legacy bool
	if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM pragma_table_info('tiers') WHERE name='price_centavos')").Scan(&legacy); err != nil {
		return err
	}
	if !legacy {
		return nil
	}
	if _, err := db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	err := func() error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, table := range []string{"tiers", "booking_items", "payments", "receipts", "refunds"} {
			prefix := "CREATE TABLE IF NOT EXISTS " + table + "("
			var create string
			for _, statement := range strings.Split(sqlText, ";") {
				statement = strings.TrimSpace(statement)
				if strings.HasPrefix(statement, prefix) {
					create = statement
					break
				}
			}
			if create == "" {
				return fmt.Errorf("missing peso schema for %s", table)
			}
			temp := "peso_migration_" + table
			if _, err := tx.Exec(strings.Replace(create, prefix, "CREATE TABLE "+temp+"(", 1)); err != nil {
				return err
			}
			rows, err := tx.Query("PRAGMA table_info(" + table + ")")
			if err != nil {
				return err
			}
			var columns, values []string
			for rows.Next() {
				var cid, notNull, primaryKey int
				var name, kind string
				var defaultValue any
				if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
					rows.Close()
					return err
				}
				newName, value := name, name
				if strings.HasSuffix(name, "_centavos") {
					newName = strings.TrimSuffix(name, "_centavos") + "_pesos"
					value = "printf('%d.%02d', " + name + "/100, " + name + "%100)"
				}
				columns = append(columns, newName)
				values = append(values, value)
			}
			rowErr := rows.Err()
			rows.Close()
			if rowErr != nil {
				return rowErr
			}
			if _, err := tx.Exec("INSERT INTO " + temp + "(" + strings.Join(columns, ",") + ") SELECT " + strings.Join(values, ",") + " FROM " + table); err != nil {
				return err
			}
			if _, err := tx.Exec("DROP TABLE " + table); err != nil {
				return err
			}
			if _, err := tx.Exec("ALTER TABLE " + temp + " RENAME TO " + table); err != nil {
				return err
			}
		}
		rows, err := tx.Query("PRAGMA foreign_key_check")
		if err != nil {
			return err
		}
		broken := rows.Next()
		rowErr := rows.Err()
		rows.Close()
		if rowErr != nil {
			return rowErr
		}
		if broken {
			return fmt.Errorf("peso migration would break database relationships")
		}
		return tx.Commit()
	}()
	_, restoreErr := db.Exec("PRAGMA foreign_keys=ON")
	if err != nil {
		return err
	}
	return restoreErr
}
