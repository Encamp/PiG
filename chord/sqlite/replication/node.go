// Package replication holds the connection wrapper the SQLite replication engine builds on. It is the Go counterpart of
// Pi's chord sqlite/replication/node module, which the package does not export yet.
package replication

// Ports packages/chord/src/sqlite/replication/node.ts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	chordsqlite "github.com/MichaelKinsy/PiG/chord/sqlite"
	"github.com/MichaelKinsy/PiG/chord/sqlite/node"
)

// ReplicationHooks is what the replication engine observes on a replicated connection. See ReplicatedNodeSqliteDatabase.
type ReplicationHooks interface {
	// AfterOperation is called synchronously after any operation through the connection settles, whether it succeeded or
	// not, because it may have committed. It must not panic and must not call the database.
	AfterOperation()
	// BeforeClose runs during Close, alone on the connection, after every earlier operation and before SQLite
	// checkpoints and deletes the WAL. Everything the engine has not taken out of the WAL by the time this returns is
	// lost to it.
	BeforeClose(executor chordsqlite.SqliteExecutor) error
}

// ReplicatedNodeSqliteDatabaseOptions are the connection settings for a replicated database file. Automatic WAL
// checkpoints are always off.
type ReplicatedNodeSqliteDatabaseOptions struct {
	// BusyTimeoutMs is the time SQLite waits for a competing file lock; see node.NodeSqliteDatabaseOptions.BusyTimeoutMs.
	BusyTimeoutMs *int
}

var errClosed = errors.New("Replicated SQLite database is closed")

// ReplicatedNodeSqliteDatabase is a Node SQLite connection whose write-ahead log (WAL) the replication engine reads from
// outside SQLite.
//
// The engine ships the pages SQLite appends to `{path}-wal`. That only works if no page leaves the WAL before the engine
// has read it, and a page leaves the WAL whenever anything checkpoints: SQLite's automatic checkpoint after a commit, a
// checkpoint by another connection, and the checkpoint SQLite runs when the last connection closes. This wrapper exists
// so that the engine, and only the engine, decides when that happens on this connection:
//
//   - It opens the file itself, with automatic checkpoints disabled, so that the engine is attached before anyone else
//     can write. pi-durable runs schema migrations on open, and those commits must be replicated like any other.
//   - It owns the underlying connection and never hands it out, so every operation goes through the wrapper. After each
//     one it calls AfterOperation, which is how the engine learns that the WAL may have grown. That includes statements
//     outside a transaction, which commit on their own.
//   - It exposes Exclusive so the engine can checkpoint, or hold back every later operation, without interleaving with a
//     commit.
//   - Close first runs BeforeClose in the connection's queue, after everything queued earlier, and only then lets the
//     connection close. Operations called after Close fail, so nothing can commit between the two steps.
//
// The wrapper does not read the WAL, schedule flushes or checkpoint; that is the engine's work, through the hooks. It also
// cannot stop another process from opening the file, which may checkpoint behind the engine's back. The engine detects
// that from the WAL salts and recovers by taking a new base.
//
// The only way to get an instance is OpenReplicatedNodeSqliteDatabase: a constructor that accepted any NodeSqliteDatabase
// would let a caller pass one with automatic checkpoints on, or keep writing through the connection directly, and either
// would drop WAL pages before the engine read them.
type ReplicatedNodeSqliteDatabase struct {
	// Path is the absolute path of the database file. The WAL is Path + "-wal".
	Path     string
	database *node.NodeSqliteDatabase
	hooks    ReplicationHooks

	mu      sync.Mutex
	settled *sync.Cond
	// inflight counts the operations that began before Close; Close waits for them, so they run before BeforeClose.
	inflight int
	closing  bool

	closeOnce sync.Once
	closeErr  error
}

// begin admits an operation, or reports that Close already started. Calls are admitted in the order they reach begin, the
// order in which they enter the connection's queue.
func (database *ReplicatedNodeSqliteDatabase) begin() error {
	database.mu.Lock()
	defer database.mu.Unlock()
	if database.closing {
		return errClosed
	}
	database.inflight++
	return nil
}

func (database *ReplicatedNodeSqliteDatabase) end() {
	database.mu.Lock()
	defer database.mu.Unlock()
	database.inflight--
	if database.inflight == 0 {
		database.settled.Broadcast()
	}
}

// observe runs operation on the connection and then calls AfterOperation, which fires whether the operation failed or not.
func (database *ReplicatedNodeSqliteDatabase) observe(operation func() error) error {
	if err := database.begin(); err != nil {
		return err
	}
	defer database.end()
	err := operation()
	database.hooks.AfterOperation()
	return err
}

func (database *ReplicatedNodeSqliteDatabase) Exec(sql string) error {
	return database.observe(func() error { return database.database.Exec(sql) })
}

func (database *ReplicatedNodeSqliteDatabase) Run(sql string, params ...chordsqlite.SqliteValue) error {
	return database.observe(func() error { return database.database.Run(sql, params...) })
}

func (database *ReplicatedNodeSqliteDatabase) Get(sql string, params ...chordsqlite.SqliteValue) (row chordsqlite.SqliteRow, err error) {
	err = database.observe(func() (operationErr error) {
		row, operationErr = database.database.Get(sql, params...)
		return operationErr
	})
	return row, err
}

func (database *ReplicatedNodeSqliteDatabase) All(sql string, params ...chordsqlite.SqliteValue) (rows []chordsqlite.SqliteRow, err error) {
	err = database.observe(func() (operationErr error) {
		rows, operationErr = database.database.All(sql, params...)
		return operationErr
	})
	return rows, err
}

func (database *ReplicatedNodeSqliteDatabase) Transaction(callback func(transaction chordsqlite.SqliteExecutor) error) error {
	return database.observe(func() error { return database.database.Transaction(callback) })
}

// Exclusive runs operation alone on the connection; see node.NodeSqliteDatabase.Exclusive. It is not an observed operation.
func (database *ReplicatedNodeSqliteDatabase) Exclusive(operation func(executor chordsqlite.SqliteExecutor) error) error {
	if err := database.begin(); err != nil {
		return err
	}
	defer database.end()
	return database.database.Exclusive(operation)
}

// Close waits for every operation called before it, runs BeforeClose alone on the connection, then closes the connection,
// which checkpoints and deletes the WAL. Every call returns the outcome of the one close.
func (database *ReplicatedNodeSqliteDatabase) Close() error {
	database.mu.Lock()
	database.closing = true
	database.mu.Unlock()
	database.closeOnce.Do(func() { database.closeErr = database.close() })
	return database.closeErr
}

func (database *ReplicatedNodeSqliteDatabase) close() error {
	database.mu.Lock()
	for database.inflight > 0 {
		database.settled.Wait()
	}
	database.mu.Unlock()
	hookErr := database.database.Exclusive(database.hooks.BeforeClose)
	if closeErr := database.database.Close(); closeErr != nil {
		if hookErr != nil {
			return &node.AggregateError{Errors: []error{hookErr, closeErr}, Message: "Replication close hook and close failed"}
		}
		return closeErr
	}
	return hookErr
}

// OpenReplicatedNodeSqliteDatabase opens a database file for replication. The file needs a WAL, so in-memory and
// temporary databases are refused.
func OpenReplicatedNodeSqliteDatabase(path string, hooks ReplicationHooks, options ReplicatedNodeSqliteDatabaseOptions) (*ReplicatedNodeSqliteDatabase, error) {
	if path == "" || path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil, fmt.Errorf("Replication needs a database file path, got %s", jsonString(path))
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	noCheckpoint := 0
	database, err := node.OpenNodeSqliteDatabase(absolute, node.NodeSqliteDatabaseOptions{
		WalAutoCheckpointPages: &noCheckpoint,
		BusyTimeoutMs:          options.BusyTimeoutMs,
	})
	if err != nil {
		return nil, err
	}
	replicated := &ReplicatedNodeSqliteDatabase{Path: absolute, database: database, hooks: hooks}
	replicated.settled = sync.NewCond(&replicated.mu)
	return replicated, nil
}

// jsonString is JSON.stringify of a string: no HTML escaping.
func jsonString(text string) string {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(text)
	return strings.TrimSuffix(encoded.String(), "\n")
}
