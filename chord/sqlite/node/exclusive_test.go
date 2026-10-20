package node

// pi: packages/chord/src/sqlite/node.ts

// Ports packages/chord/test/sqlite/node.test.ts (Pi 48c8f322) "runs an exclusive operation alone, outside a transaction, after earlier calls"
// and "rolls back a transaction an exclusive operation left open and rejects".

import (
	"errors"
	"testing"
	"testing/synctest"

	chordsqlite "github.com/MichaelKinsy/PiG/chord/sqlite"
)

func TestNodeSqliteExclusiveRunsAloneOutsideATransactionAfterEarlierCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		database := mustOpenMemory(t)
		mustDo(t, database.Exec("CREATE TABLE exclusive_probe (value INTEGER)"))
		earlier := startErr(func() error { return database.Run("INSERT INTO exclusive_probe (value) VALUES (?)", 1) })
		synctest.Wait()
		gate := make(chan struct{})
		var handle chordsqlite.SqliteExecutor
		var seen []chordsqlite.SqliteRow
		exclusive := startErr(func() error {
			return database.Exclusive(func(executor chordsqlite.SqliteExecutor) error {
				handle = executor
				var err error
				if seen, err = executor.All("SELECT value FROM exclusive_probe"); err != nil {
					return err
				}
				// Not inside a transaction: the handle may begin and commit one itself.
				if err := executor.Exec("BEGIN; INSERT INTO exclusive_probe (value) VALUES (2); COMMIT"); err != nil {
					return err
				}
				<-gate
				return nil
			})
		})
		synctest.Wait()
		later := start(func() ([]chordsqlite.SqliteRow, error) {
			return database.All("SELECT value FROM exclusive_probe ORDER BY value")
		})
		synctest.Wait()
		_, err := earlier.await()
		mustDo(t, err)
		if later.settled() {
			t.Fatal("a call after the exclusive operation ran before it settled")
		}
		if database.access.queued() != 1 {
			t.Fatalf("queued operations = %d, want 1", database.access.queued())
		}

		close(gate)
		_, err = exclusive.await()
		mustDo(t, err)
		expectValues(t, values(seen), []int64{1})
		rows, err := later.await()
		mustDo(t, err)
		expectValues(t, values(rows), []int64{1, 2})
		expectError(t, handle.Exec("SELECT 1"), "SQLite exclusive handle is no longer active")
		expectError(t, database.Exclusive(func(chordsqlite.SqliteExecutor) error { return errors.New("failed") }), "failed")
		row, err := database.Get("SELECT count(*) AS count FROM exclusive_probe")
		mustDo(t, err)
		if row["count"] != int64(2) {
			t.Fatalf("count = %v, want 2", row["count"])
		}
		mustDo(t, database.Close())
	})
}

func TestNodeSqliteExclusiveRollsBackATransactionItLeftOpenAndFails(t *testing.T) {
	database := mustOpenMemory(t)
	mustDo(t, database.Exec("CREATE TABLE left_open (value INTEGER)"))
	const leftOpen = "SQLite exclusive operation left a transaction open; it was rolled back"

	err := database.Exclusive(func(executor chordsqlite.SqliteExecutor) error {
		return executor.Exec("BEGIN; INSERT INTO left_open (value) VALUES (1)")
	})
	expectError(t, err, leftOpen)
	if errors.Unwrap(err) != nil {
		t.Fatalf("an operation that succeeded has no cause: %v", errors.Unwrap(err))
	}

	failure := errors.New("failed mid-transaction")
	err = database.Exclusive(func(executor chordsqlite.SqliteExecutor) error {
		if err := executor.Exec("BEGIN IMMEDIATE; INSERT INTO left_open (value) VALUES (2)"); err != nil {
			return err
		}
		return failure
	})
	expectError(t, err, leftOpen)
	if !errors.Is(err, failure) {
		t.Fatalf("Exclusive() = %v, want the operation's error as the cause", err)
	}

	// Later calls run outside any leftover transaction: autocommit writes commit and transactions can begin.
	mustDo(t, database.Run("INSERT INTO left_open (value) VALUES (?)", 3))
	mustDo(t, database.Transaction(func(transaction chordsqlite.SqliteExecutor) error {
		return transaction.Run("INSERT INTO left_open (value) VALUES (?)", 4)
	}))
	rows, err := database.All("SELECT value FROM left_open ORDER BY value")
	mustDo(t, err)
	expectValues(t, values(rows), []int64{3, 4})
	mustDo(t, database.Close())
}

// A panic inside the operation, like a rejection in Pi, must not leave the operation's transaction open for the calls after it.
func TestNodeSqliteExclusiveRollsBackWhenTheOperationPanics(t *testing.T) {
	database := mustOpenMemory(t)
	mustDo(t, database.Exec("CREATE TABLE panicked (value INTEGER)"))
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the panic did not continue")
			}
		}()
		_ = database.Exclusive(func(executor chordsqlite.SqliteExecutor) error {
			mustDo(t, executor.Exec("BEGIN; INSERT INTO panicked (value) VALUES (1)"))
			panic("boom")
		})
	}()
	mustDo(t, database.Run("INSERT INTO panicked (value) VALUES (?)", 2))
	rows, err := database.All("SELECT value FROM panicked ORDER BY value")
	mustDo(t, err)
	expectValues(t, values(rows), []int64{2})
	mustDo(t, database.Close())
}
