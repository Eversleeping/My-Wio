//go:build ignore

// Export the structure produced by a historical Store on PostgreSQL 16.
// See the P1-01 execution record for archive and reproduction commands.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func main() {
	if len(os.Args) != 4 || (os.Args[1] != "init" && os.Args[1] != "dump") {
		log.Fatal("usage: pgfixture init|dump DATABASE_URL SCHEMA")
	}
	if !identifier.MatchString(os.Args[3]) {
		log.Fatal("invalid schema")
	}
	db, err := sql.Open("pgx", os.Args[2])
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	// Keep SET search_path and all catalog queries on the same connection.
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if os.Args[1] == "init" {
		_, err = db.ExecContext(ctx, `CREATE SCHEMA `+os.Args[3])
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := dump(ctx, db, os.Args[3]); err != nil {
		log.Fatal(err)
	}
}

func dump(ctx context.Context, db *sql.DB, schema string) error {
	if _, err := db.ExecContext(ctx, `SET search_path TO `+schema); err != nil {
		return err
	}
	tables, err := queryStrings(ctx, db, `SELECT quote_ident(c.relname)
		FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=$1 AND c.relkind='r' ORDER BY c.relname`, schema)
	if err != nil {
		return err
	}
	for _, table := range tables {
		rows, err := db.QueryContext(ctx, `SELECT quote_ident(a.attname), pg_catalog.format_type(a.atttypid,a.atttypmod),
			a.attnotnull, COALESCE(pg_get_expr(d.adbin,d.adrelid),'')
			FROM pg_attribute a
			JOIN pg_class c ON c.oid=a.attrelid
			JOIN pg_namespace n ON n.oid=c.relnamespace
			LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
			WHERE n.nspname=$1 AND c.relname=$2 AND a.attnum>0 AND NOT a.attisdropped
			ORDER BY a.attnum`, schema, strings.Trim(table, `"`))
		if err != nil {
			return err
		}
		var columns []string
		for rows.Next() {
			var name, dataType, defaultExpression string
			var notNull bool
			if err := rows.Scan(&name, &dataType, &notNull, &defaultExpression); err != nil {
				rows.Close()
				return err
			}
			definition := "  " + name + " " + dataType
			if notNull {
				definition += " NOT NULL"
			}
			if defaultExpression != "" {
				definition += " DEFAULT " + defaultExpression
			}
			columns = append(columns, definition)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		fmt.Printf("CREATE TABLE %s (\n%s\n);\n", table, strings.Join(columns, ",\n"))
	}
	constraints, err := queryStrings(ctx, db, `SELECT 'ALTER TABLE '||quote_ident(c.relname)||' ADD CONSTRAINT '||quote_ident(con.conname)||' '||pg_get_constraintdef(con.oid,true)||';'
		FROM pg_constraint con JOIN pg_class c ON c.oid=con.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=$1 ORDER BY CASE WHEN con.contype='f' THEN 1 ELSE 0 END,c.relname,con.conname`, schema)
	if err != nil {
		return err
	}
	for _, statement := range constraints {
		fmt.Println(strings.ReplaceAll(statement, schema+".", ""))
	}
	indexes, err := queryStrings(ctx, db, `SELECT pg_get_indexdef(i.indexrelid)||';'
		FROM pg_index i JOIN pg_class c ON c.oid=i.indrelid JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=$1 AND NOT EXISTS (SELECT 1 FROM pg_constraint con WHERE con.conindid=i.indexrelid)
		ORDER BY c.relname,(SELECT relname FROM pg_class WHERE oid=i.indexrelid)`, schema)
	if err != nil {
		return err
	}
	for _, statement := range indexes {
		fmt.Println(strings.ReplaceAll(statement, schema+".", ""))
	}
	return nil
}

func queryStrings(ctx context.Context, db *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
