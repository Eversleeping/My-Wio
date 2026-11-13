package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
)

//go:embed testdata/schema/sqlite/*.sql testdata/schema/postgres/*.sql
var schemaFixtures embed.FS

var schemaFixtureVersions = []string{"0.2.45", "0.2.58", "0.3.12"}

type inspectedSchema struct {
	Tables      map[string]inspectedTable
	Indexes     map[string]inspectedIndex
	ForeignKeys map[string]string
}

type inspectedTable struct {
	Columns map[string]inspectedColumn
}

type inspectedColumn struct {
	Type       string
	NotNull    bool
	Default    string
	PrimaryKey int
}

type inspectedIndex struct {
	Table      string
	Unique     bool
	Columns    []string
	Predicate  string
	Definition string
}

type schemaDifference struct {
	Path     string
	Upgraded string
	Fresh    string
}

type knownSchemaDivergence struct {
	Database string
	Fixture  string
	Path     string
	Upgraded string
	Fresh    string
	Reason   string
}

// Reserve exact, documented exceptions for observed migration differences. There
// are currently no known differences for the three fixtures in either database.
var knownSchemaDivergences = []knownSchemaDivergence{}

func TestSQLiteFixtureSchemaConsistency(t *testing.T) {
	for _, version := range schemaFixtureVersions {
		version := version
		t.Run(version, func(t *testing.T) {
			fixture, err := schemaFixtures.ReadFile("testdata/schema/sqlite/" + version + ".sql")
			if err != nil {
				t.Fatal(err)
			}
			migratedPath := filepath.Join(t.TempDir(), "migrated.db")
			legacy, err := sql.Open("sqlite", migratedPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := legacy.Exec(string(fixture)); err != nil {
				_ = legacy.Close()
				t.Fatalf("apply SQLite %s fixture: %v", version, err)
			}
			if err := legacy.Close(); err != nil {
				t.Fatal(err)
			}

			migrated := openSQLiteFixtureStore(t, migratedPath)
			first := inspectSQLiteSchema(t, migrated.DB)
			if err := migrated.Close(); err != nil {
				t.Fatal(err)
			}
			migrated = openSQLiteFixtureStore(t, migratedPath)
			second := inspectSQLiteSchema(t, migrated.DB)
			if err := migrated.Close(); err != nil {
				t.Fatal(err)
			}
			assertSchemaDifferences(t, "sqlite", version+" second open", compareSchemas(first, second), nil)

			fresh := openSQLiteFixtureStore(t, filepath.Join(t.TempDir(), "fresh.db"))
			freshSchema := inspectSQLiteSchema(t, fresh.DB)
			if err := fresh.Close(); err != nil {
				t.Fatal(err)
			}
			assertSchemaDifferences(t, "sqlite", version, compareSchemas(first, freshSchema), knownDivergences("sqlite", version))
		})
	}
}

func openSQLiteFixtureStore(t *testing.T, path string) *Store {
	t.Helper()
	database, err := Open(path + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func inspectSQLiteSchema(t *testing.T, db *sqlx.DB) inspectedSchema {
	t.Helper()
	ctx := context.Background()
	result := newInspectedSchema()
	var tables []string
	if err := db.SelectContext(ctx, &tables, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`); err != nil {
		t.Fatal(err)
	}
	for _, tableName := range tables {
		var rows []struct {
			Name       string         `db:"name"`
			Type       string         `db:"type"`
			NotNull    int            `db:"not_null"`
			Default    sql.NullString `db:"default_value"`
			PrimaryKey int            `db:"primary_key"`
		}
		if err := db.SelectContext(ctx, &rows, `SELECT name,type,"notnull" AS not_null,dflt_value AS default_value,pk AS primary_key FROM pragma_table_info(?) ORDER BY cid`, tableName); err != nil {
			t.Fatal(err)
		}
		table := inspectedTable{Columns: make(map[string]inspectedColumn, len(rows))}
		for _, row := range rows {
			table.Columns[row.Name] = inspectedColumn{
				Type:       strings.ToUpper(strings.TrimSpace(row.Type)),
				NotNull:    row.NotNull != 0,
				Default:    normalizeSQL(row.Default.String),
				PrimaryKey: row.PrimaryKey,
			}
		}
		result.Tables[tableName] = table

		var indexes []struct {
			Name    string `db:"name"`
			Unique  int    `db:"is_unique"`
			Partial int    `db:"partial"`
		}
		if err := db.SelectContext(ctx, &indexes, `SELECT name,"unique" AS is_unique,partial FROM pragma_index_list(?) ORDER BY name`, tableName); err != nil {
			t.Fatal(err)
		}
		for _, index := range indexes {
			var columns []struct {
				Sequence   int            `db:"seqno"`
				ColumnID   int            `db:"cid"`
				Name       sql.NullString `db:"name"`
				Descending int            `db:"desc"`
				Collation  sql.NullString `db:"coll"`
				Key        int            `db:"key"`
			}
			if err := db.SelectContext(ctx, &columns, `SELECT seqno,cid,name,"desc",coll,key FROM pragma_index_xinfo(?) ORDER BY seqno`, index.Name); err != nil {
				t.Fatal(err)
			}
			definition := inspectedIndex{Table: tableName, Unique: index.Unique != 0}
			for _, column := range columns {
				if column.Key == 0 {
					continue
				}
				name := column.Name.String
				if !column.Name.Valid {
					name = fmt.Sprintf("<expression:%d>", column.Sequence)
				}
				name += " COLLATE " + column.Collation.String
				if column.Descending != 0 {
					name += " DESC"
				} else {
					name += " ASC"
				}
				definition.Columns = append(definition.Columns, name)
			}
			var statement sql.NullString
			if err := db.GetContext(ctx, &statement, `SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, index.Name); err != nil {
				t.Fatal(err)
			}
			predicate, err := sqliteIndexPredicate(statement.String, index.Partial != 0)
			if err != nil {
				t.Fatalf("inspect SQLite index %s on %s: %v", index.Name, tableName, err)
			}
			definition.Predicate = predicate
			result.Indexes[index.Name] = definition
		}

		var foreignKeyRows []struct {
			ID       int    `db:"id"`
			Sequence int    `db:"seq"`
			Table    string `db:"table"`
			From     string `db:"from"`
			To       string `db:"to"`
			OnUpdate string `db:"on_update"`
			OnDelete string `db:"on_delete"`
			Match    string `db:"match"`
		}
		if err := db.SelectContext(ctx, &foreignKeyRows, `SELECT id,seq,"table","from","to",on_update,on_delete,"match" FROM pragma_foreign_key_list(?) ORDER BY id,seq`, tableName); err != nil {
			t.Fatal(err)
		}
		type foreignKeyGroup struct {
			Table, OnUpdate, OnDelete, Match string
			From, To                         []string
		}
		groups := map[int]*foreignKeyGroup{}
		for _, row := range foreignKeyRows {
			group := groups[row.ID]
			if group == nil {
				group = &foreignKeyGroup{Table: row.Table, OnUpdate: row.OnUpdate, OnDelete: row.OnDelete, Match: row.Match}
				groups[row.ID] = group
			}
			group.From = append(group.From, row.From)
			group.To = append(group.To, row.To)
		}
		for _, group := range groups {
			key := tableName + "(" + strings.Join(group.From, ",") + ")"
			result.ForeignKeys[key] = fmt.Sprintf("REFERENCES %s(%s) ON UPDATE %s ON DELETE %s MATCH %s", group.Table, strings.Join(group.To, ","), group.OnUpdate, group.OnDelete, group.Match)
		}
	}
	return result
}

func newInspectedSchema() inspectedSchema {
	return inspectedSchema{Tables: map[string]inspectedTable{}, Indexes: map[string]inspectedIndex{}, ForeignKeys: map[string]string{}}
}

func compareSchemas(upgraded, fresh inspectedSchema) []schemaDifference {
	var differences []schemaDifference
	for _, tableName := range unionKeys(upgraded.Tables, fresh.Tables) {
		upgradedTable, upgradedOK := upgraded.Tables[tableName]
		freshTable, freshOK := fresh.Tables[tableName]
		if !upgradedOK || !freshOK {
			differences = append(differences, schemaDifference{Path: "table " + tableName, Upgraded: presence(upgradedOK), Fresh: presence(freshOK)})
			continue
		}
		for _, columnName := range unionKeys(upgradedTable.Columns, freshTable.Columns) {
			upgradedColumn, upgradedOK := upgradedTable.Columns[columnName]
			freshColumn, freshOK := freshTable.Columns[columnName]
			path := "column " + tableName + "." + columnName
			if !upgradedOK || !freshOK {
				differences = append(differences, schemaDifference{Path: path, Upgraded: presence(upgradedOK), Fresh: presence(freshOK)})
				continue
			}
			compareAttribute(&differences, path+" type", upgradedColumn.Type, freshColumn.Type)
			compareAttribute(&differences, path+" not-null", strconv.FormatBool(upgradedColumn.NotNull), strconv.FormatBool(freshColumn.NotNull))
			compareAttribute(&differences, path+" default", upgradedColumn.Default, freshColumn.Default)
			compareAttribute(&differences, path+" primary-key-position", strconv.Itoa(upgradedColumn.PrimaryKey), strconv.Itoa(freshColumn.PrimaryKey))
		}
	}
	for _, indexName := range unionKeys(upgraded.Indexes, fresh.Indexes) {
		upgradedIndex, upgradedOK := upgraded.Indexes[indexName]
		freshIndex, freshOK := fresh.Indexes[indexName]
		path := "index " + indexName
		if !upgradedOK || !freshOK {
			tableName := upgradedIndex.Table
			if !upgradedOK {
				tableName = freshIndex.Table
			}
			path += " on " + tableName
			differences = append(differences, schemaDifference{Path: path, Upgraded: presence(upgradedOK), Fresh: presence(freshOK)})
			continue
		}
		compareAttribute(&differences, path+" table", upgradedIndex.Table, freshIndex.Table)
		compareAttribute(&differences, path+" unique", strconv.FormatBool(upgradedIndex.Unique), strconv.FormatBool(freshIndex.Unique))
		compareAttribute(&differences, path+" columns", strings.Join(upgradedIndex.Columns, ", "), strings.Join(freshIndex.Columns, ", "))
		compareAttribute(&differences, path+" predicate", upgradedIndex.Predicate, freshIndex.Predicate)
		compareAttribute(&differences, path+" definition", upgradedIndex.Definition, freshIndex.Definition)
	}
	for _, key := range unionKeys(upgraded.ForeignKeys, fresh.ForeignKeys) {
		upgradedForeignKey, upgradedOK := upgraded.ForeignKeys[key]
		freshForeignKey, freshOK := fresh.ForeignKeys[key]
		if !upgradedOK {
			upgradedForeignKey = "<missing>"
		}
		if !freshOK {
			freshForeignKey = "<missing>"
		}
		compareAttribute(&differences, "foreign key "+key, upgradedForeignKey, freshForeignKey)
	}
	sort.Slice(differences, func(i, j int) bool { return differences[i].Path < differences[j].Path })
	return differences
}

func compareAttribute(differences *[]schemaDifference, path, upgraded, fresh string) {
	if upgraded != fresh {
		*differences = append(*differences, schemaDifference{Path: path, Upgraded: upgraded, Fresh: fresh})
	}
}

func assertSchemaDifferences(t *testing.T, database, fixture string, actual []schemaDifference, expected []knownSchemaDivergence) {
	t.Helper()
	expectedByPath := make(map[string]knownSchemaDivergence, len(expected))
	for _, divergence := range expected {
		expectedByPath[divergence.Path] = divergence
	}
	var unexpected []string
	seen := map[string]bool{}
	for _, difference := range actual {
		known, ok := expectedByPath[difference.Path]
		if !ok || known.Upgraded != difference.Upgraded || known.Fresh != difference.Fresh {
			unexpected = append(unexpected, fmt.Sprintf("%s: upgraded=%q fresh=%q", difference.Path, difference.Upgraded, difference.Fresh))
			continue
		}
		seen[difference.Path] = true
	}
	for _, divergence := range expected {
		if !seen[divergence.Path] {
			unexpected = append(unexpected, fmt.Sprintf("known divergence disappeared or changed: %s (expected upgraded=%q fresh=%q)", divergence.Path, divergence.Upgraded, divergence.Fresh))
		}
	}
	if len(unexpected) != 0 {
		t.Fatalf("%s fixture %s schema differs:\n  %s", database, fixture, strings.Join(unexpected, "\n  "))
	}
}

func knownDivergences(database, fixture string) []knownSchemaDivergence {
	var result []knownSchemaDivergence
	for _, divergence := range knownSchemaDivergences {
		if divergence.Database == database && divergence.Fixture == fixture {
			result = append(result, divergence)
		}
	}
	return result
}

func unionKeys[V any](left, right map[string]V) []string {
	keys := make(map[string]struct{}, len(left)+len(right))
	for key := range left {
		keys[key] = struct{}{}
	}
	for key := range right {
		keys[key] = struct{}{}
	}
	result := make([]string, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func presence(ok bool) string {
	if ok {
		return "present"
	}
	return "<missing>"
}

func normalizeSQL(value string) string {
	// Preserve whitespace inside string literals: it is part of default values
	// and predicates, and collapsing it could hide a real structure difference.
	return strings.TrimSpace(value)
}

// Tokenize quoted strings/identifiers and comments as indivisible tokens, so a
// WHERE inside one of those or inside an index expression is not the predicate.
var sqliteIndexSQLTokens = regexp.MustCompile("(?s)'(?:''|[^'])*'|\"(?:\"\"|[^\"])*\"|`(?:``|[^`])*`|\\[[^\\]]*\\]|--[^\\r\\n]*|/\\*.*?\\*/|[[:alpha:]_][[:alnum:]_$]*|[()]")

func sqliteIndexPredicate(statement string, partial bool) (string, error) {
	depth := 0
	predicate := ""
	found := false
	for _, location := range sqliteIndexSQLTokens.FindAllStringIndex(statement, -1) {
		token := statement[location[0]:location[1]]
		switch token {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth == 0 && strings.EqualFold(token, "WHERE") {
			predicate = normalizeSQL(statement[location[1]:])
			found = true
			break
		}
	}
	if partial != found || (partial && predicate == "") {
		return "", fmt.Errorf("partial=%t but predicate found=%t, predicate=%q in SQL %q", partial, found, predicate, statement)
	}
	return predicate, nil
}

func TestSQLiteIndexPredicate(t *testing.T) {
	for _, test := range []struct {
		name, statement, want string
		partial, wantError    bool
	}{
		{name: "single line", statement: "CREATE INDEX idx ON events(kind) WHERE kind='a  b'", partial: true, want: "kind='a  b'"},
		{name: "newlines and tabs", statement: "CREATE INDEX idx ON events(kind)\n\twHeRe\t\nkind='a  b'", partial: true, want: "kind='a  b'"},
		{name: "quoted and commented keywords", statement: "CREATE INDEX \"WHERE\" ON events((kind || 'WHERE(')) /* WHERE ignored */\nWHERE kind='real'", partial: true, want: "kind='real'"},
		{name: "non partial quoted keyword", statement: "CREATE INDEX `WHERE` ON events([WHERE], (kind || 'it''s WHERE')) -- WHERE ignored"},
		{name: "implicit index"},
		{name: "partial without predicate", statement: "CREATE INDEX idx ON events(kind)", partial: true, wantError: true},
		{name: "non partial with predicate", statement: "CREATE INDEX idx ON events(kind)\nWHERE kind='real'", wantError: true},
		{name: "empty predicate", statement: "CREATE INDEX idx ON events(kind) WHERE\t", partial: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := sqliteIndexPredicate(test.statement, test.partial)
			if (err != nil) != test.wantError || got != test.want {
				t.Fatalf("predicate=%q err=%v; want %q error=%t", got, err, test.want, test.wantError)
			}
		})
	}
}
