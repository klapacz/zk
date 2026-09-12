package sqlite

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zk-org/zk/internal/util/opt"
	"github.com/zk-org/zk/internal/util/test/assert"
)

// testDB is an utility function to create a database loaded with the default fixtures.
func testDB(t *testing.T) *DB {
	return testDBWithFixtures(t, opt.NewString("default"))
}

// testDB is an utility function to create a database loaded with a set of DB fixtures.
func testDBWithFixtures(t *testing.T, fixturesDir opt.String) *DB {
	db, err := OpenInMemory()
	assert.Nil(t, err)

	if !fixturesDir.IsNull() {
		loadFixtures(t, db.db, "testdata/"+fixturesDir.String())
	}

	return db
}

// testTransaction is an utility function used to test a SQLite transaction to
// the DB, which loads the default set of DB fixtures.
func testTransaction(t *testing.T, test func(tx Transaction)) {
	testTransactionWithFixtures(t, opt.NewString("default"), test)
}

// testTransactionWithFixtures is an utility function used to test a SQLite transaction to
// the DB, which loads the given set of DB fixtures.
func testTransactionWithFixtures(t *testing.T, fixturesDir opt.String, test func(tx Transaction)) {
	err := testDBWithFixtures(t, fixturesDir).WithWriteTransaction(func(tx Transaction) error {
		test(tx)
		return nil
	})
	assert.Nil(t, err)
}

func assertExistOrNot(t *testing.T, db *DB, shouldExist bool, sql string, args ...any) {
	if shouldExist {
		assertExist(t, db, sql, args...)
	} else {
		assertNotExist(t, db, sql, args...)
	}
}

func assertExist(t *testing.T, db *DB, sql string, args ...any) {
	if !exists(t, db, sql, args...) {
		t.Errorf("SQL query did not return any result: %s, with arguments %v", sql, args)
	}
}

func assertNotExist(t *testing.T, db *DB, sql string, args ...any) {
	if exists(t, db, sql, args...) {
		t.Errorf("SQL query returned a result: %s, with arguments %v", sql, args)
	}
}

func exists(t *testing.T, db *DB, sql string, args ...any) bool {
	var exists int
	err := db.db.QueryRow("SELECT EXISTS ("+sql+")", args...).Scan(&exists)
	assert.Nil(t, err)
	return exists == 1
}

func assertExistTx(t *testing.T, tx Transaction, sql string, args ...any) {
	if !existsTx(t, tx, sql, args...) {
		t.Errorf("SQL query did not return any result: %s, with arguments %v", sql, args)
	}
}

func assertNotExistTx(t *testing.T, tx Transaction, sql string, args ...any) {
	if existsTx(t, tx, sql, args...) {
		t.Errorf("SQL query returned a result: %s, with arguments %v", sql, args)
	}
}

func existsTx(t *testing.T, tx Transaction, sql string, args ...any) bool {
	var exists int
	err := tx.QueryRow("SELECT EXISTS ("+sql+")", args...).Scan(&exists)
	assert.Nil(t, err)
	return exists == 1
}

func TestWriteTransactionsSerializeAcrossHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	first := openFileDB(t, path)
	second := openFileDB(t, path)

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.WithWriteTransaction(func(tx Transaction) error {
			close(firstStarted)
			<-releaseFirst
			return nil
		})
	}()
	<-firstStarted

	secondAttempted := make(chan struct{})
	secondStarted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondAttempted)
		secondDone <- second.WithWriteTransaction(func(tx Transaction) error {
			close(secondStarted)
			return nil
		})
	}()
	<-secondAttempted

	select {
	case <-secondStarted:
		t.Error("second writer entered before the first writer released its reservation")
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseFirst)
	assert.Nil(t, <-firstDone)
	select {
	case <-secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("second writer did not enter after the first writer committed")
	}
	assert.Nil(t, <-secondDone)
}

func TestReadTransactionDoesNotWaitForWriterReservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	holder := openFileDB(t, path)
	reader := openFileDB(t, path)
	reader.db.SetMaxOpenConns(1)
	_, err := reader.db.Exec("PRAGMA busy_timeout = 50")
	assert.Nil(t, err)

	holderStarted := make(chan struct{})
	releaseHolder := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- holder.WithWriteTransaction(func(tx Transaction) error {
			close(holderStarted)
			<-releaseHolder
			return nil
		})
	}()
	<-holderStarted

	err = reader.WithTransaction(func(tx Transaction) error {
		var version int
		return tx.QueryRow("PRAGMA user_version").Scan(&version)
	})
	assert.Nil(t, err)

	close(releaseHolder)
	assert.Nil(t, <-holderDone)
}

func TestWriteTransactionLockFailureIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	holder := openFileDB(t, path)
	contender := openFileDB(t, path)
	contender.db.SetMaxOpenConns(1)
	_, err := contender.db.Exec("PRAGMA busy_timeout = 50")
	assert.Nil(t, err)

	holderStarted := make(chan struct{})
	releaseHolder := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- holder.WithWriteTransaction(func(tx Transaction) error {
			close(holderStarted)
			<-releaseHolder
			return nil
		})
	}()
	<-holderStarted

	callbackCalled := false
	start := time.Now()
	err = contender.WithWriteTransaction(func(tx Transaction) error {
		callbackCalled = true
		return nil
	})
	duration := time.Since(start)

	assert.Err(t, err, "begin immediate transaction")
	assert.False(t, callbackCalled)
	if duration < 25*time.Millisecond || duration > time.Second {
		t.Errorf("writer lock failure took %s, expected a bounded wait near the configured timeout", duration)
	}

	close(releaseHolder)
	assert.Nil(t, <-holderDone)
}

func TestWithTransactionReturnsCommitError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	writer := openFileDB(t, path)
	reader := openFileDB(t, path)
	insertCommitTestNote(t, writer)
	writer.db.SetMaxOpenConns(1)
	_, err := writer.db.Exec("PRAGMA busy_timeout = 50")
	assert.Nil(t, err)
	releaseReader := holdCommitTestReadLock(t, reader)

	err = writer.WithTransaction(func(tx Transaction) error {
		_, err := tx.Exec("UPDATE notes SET title = 'changed' WHERE path = 'commit.md'")
		return err
	})
	assert.Err(t, err, "commit transaction")
	releaseReader()
}

func TestWriteTransactionRollsBackAfterCommitFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	writer := openFileDB(t, path)
	reader := openFileDB(t, path)
	insertCommitTestNote(t, writer)
	writer.db.SetMaxOpenConns(1)
	_, err := writer.db.Exec("PRAGMA busy_timeout = 50")
	assert.Nil(t, err)
	releaseReader := holdCommitTestReadLock(t, reader)

	err = writer.WithWriteTransaction(func(tx Transaction) error {
		_, err := tx.Exec("UPDATE notes SET title = 'changed' WHERE path = 'commit.md'")
		return err
	})
	assert.Err(t, err, "commit transaction")
	releaseReader()

	var title string
	err = writer.db.QueryRow("SELECT title FROM notes WHERE path = 'commit.md'").Scan(&title)
	assert.Nil(t, err)
	assert.Equal(t, title, "original")
}

func TestWriteTransactionClosesLazyRowsBeforeRollback(t *testing.T) {
	db := testDB(t)
	callbackErr := errors.New("stop transaction")

	err := db.WithWriteTransaction(func(tx Transaction) error {
		rows, err := tx.PrepareLazy("SELECT path FROM notes").Query()
		if err != nil {
			return err
		}
		if !rows.Next() {
			return errors.New("expected a note row")
		}
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Errorf("expected callback error, got %v", err)
	}

	err = db.WithWriteTransaction(func(tx Transaction) error {
		var count int
		return tx.QueryRow("SELECT COUNT(*) FROM notes").Scan(&count)
	})
	assert.Nil(t, err)
}

func openFileDB(t *testing.T, path string) *DB {
	t.Helper()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

func insertCommitTestNote(t *testing.T, db *DB) {
	t.Helper()
	err := db.WithWriteTransaction(func(tx Transaction) error {
		_, err := tx.Exec(`
			INSERT INTO notes (path, sortable_path, filename, title, checksum)
			VALUES ('commit.md', 'commit.md', 'commit.md', 'original', 'checksum')
		`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func holdCommitTestReadLock(t *testing.T, db *DB) func() {
	t.Helper()
	tx, err := db.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query("SELECT title FROM notes WHERE path = 'commit.md'")
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if !rows.Next() {
		_ = rows.Close()
		_ = tx.Rollback()
		t.Fatal("commit test note was not found")
	}
	var title string
	if err := rows.Scan(&title); err != nil {
		_ = rows.Close()
		_ = tx.Rollback()
		t.Fatal(err)
	}

	return func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
		if err := tx.Rollback(); err != nil {
			t.Error(err)
		}
	}
}
