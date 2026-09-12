package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/zk-org/zk/internal/util/fixtures"
	"github.com/zk-org/zk/internal/util/test/assert"
)

func TestOpen(t *testing.T) {
	_, err := Open(fixtures.Path("sample.db"))
	assert.Nil(t, err)
}

func TestClose(t *testing.T) {
	db, err := Open(fixtures.Path("sample.db"))
	assert.Nil(t, err)
	err = db.Close()
	assert.Nil(t, err)
}

func TestMigrateFrom0(t *testing.T) {
	db, err := OpenInMemory()
	assert.Nil(t, err)

	err = db.WithWriteTransaction(func(tx Transaction) error {
		var version int
		err := tx.QueryRow("PRAGMA user_version").Scan(&version)
		assert.Nil(t, err)
		assert.Equal(t, version, 8)

		_, err = tx.Exec(`
			INSERT INTO notes (path, sortable_path, filename, title, body, word_count, checksum)
			VALUES ("ref/tx1.md", "reftx1.md", "tx1.md", "A reference", "Content", 1, "qwfpg")
		`)
		assert.Nil(t, err)

		return nil
	})
	assert.Nil(t, err)
}

func TestConcurrentOpenWaitsForMigrationWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	native, err := sql.Open("sqlite3_custom", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	native.SetMaxOpenConns(1)
	_, err = native.Exec("BEGIN IMMEDIATE")
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		db  *DB
		err error
	}
	attempted := make(chan struct{}, 2)
	results := make(chan result, 2)
	for range 2 {
		go func() {
			attempted <- struct{}{}
			db, err := Open(path)
			results <- result{db: db, err: err}
		}()
	}
	<-attempted
	<-attempted
	<-time.After(25 * time.Millisecond)

	_, commitErr := native.Exec("COMMIT")
	assert.Nil(t, commitErr)
	for range 2 {
		res := <-results
		if res.err != nil {
			t.Errorf("concurrent database opening failed: %v", res.err)
			continue
		}

		var version int
		err := res.db.db.QueryRow("PRAGMA user_version").Scan(&version)
		assert.Nil(t, err)
		assert.Equal(t, version, 8)
		assert.Nil(t, res.db.Close())
	}
}

func TestDatabaseConnectionSettings(t *testing.T) {
	db := openFileDB(t, filepath.Join(t.TempDir(), "index_foreign_keys=off.db"))
	for range 2 {
		// Keep the first connection checked out so the pool opens another one.
		conn, err := db.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()

		var foreignKeys, busyTimeout int
		err = conn.QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&foreignKeys)
		assert.Nil(t, err)
		assert.Equal(t, foreignKeys, 1)
		err = conn.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&busyTimeout)
		assert.Nil(t, err)
		assert.Equal(t, busyTimeout, 5000)
	}
}
