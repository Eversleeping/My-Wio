//go:build postgresintegration

package store

import (
	"context"
	"database/sql"
	"net/url"
	"testing"
)

// assertOperationInterruptsUnderLinguisticCollation runs the interruption
// lifecycle in a database with an en-US collation, whatever locale the test
// cluster was initialized with. Interruption lookups once used a text range
// that only holds under byte-order collation.
func assertOperationInterruptsUnderLinguisticCollation(t *testing.T, databaseURL string) {
	t.Helper()
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	created := false
	for _, statement := range []string{
		`CREATE DATABASE wio_linguistic TEMPLATE template0 ENCODING 'UTF8' LC_COLLATE 'en_US.UTF-8' LC_CTYPE 'en_US.UTF-8'`,
		`CREATE DATABASE wio_linguistic TEMPLATE template0 ENCODING 'UTF8' LOCALE_PROVIDER icu ICU_LOCALE 'en-US' LOCALE 'C'`,
	} {
		if _, err := admin.Exec(statement); err != nil {
			t.Logf("%s: %v", statement, err)
			continue
		}
		created = true
		break
	}
	admin.Close()
	if !created {
		t.Log("no en-US collation is available; skipping the linguistic collation check")
		return
	}
	linguisticURL, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	linguisticURL.Path = "/wio_linguistic"
	database, err := Open(linguisticURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var byteOrder bool
	if err := database.DB.GetContext(context.Background(), &byteOrder, "SELECT 'operation-interrupt:a:b' < 'operation-interrupt:a;'"); err != nil {
		t.Fatal(err)
	}
	if byteOrder {
		t.Fatal("database unexpectedly compares text in byte order")
	}
	assertOperationInterruptLifecycle(t, database)
}
