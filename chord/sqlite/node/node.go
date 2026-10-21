// Package node adapts a native SQLite connection to the portable Chord SQLite facade. It is the Go counterpart of
// Pi's node:sqlite driver and the only Chord SQLite package that links a SQLite driver (modernc.org/sqlite, pure Go,
// CGO-free).
package node

// Ports packages/chord/src/sqlite/node.ts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	// Registers the pure-Go "sqlite" database/sql driver.
	_ "modernc.org/sqlite"

	chordsqlite "github.com/MichaelKinsy/PiG/chord/sqlite"
)

// DatabaseSyncOptions configures one native SQLite connection.
type DatabaseSyncOptions struct {
	// TimeoutMs is how long SQLite waits for a competing file lock; zero does not wait.
	TimeoutMs int
	// ReadOnly opens an existing database without write access.
	ReadOnly bool
}

var errDatabaseNotOpen = errors.New("database is not open")

// DatabaseSync is one native SQLite connection whose operations run on the caller's goroutine, the Go counterpart of
// node:sqlite's DatabaseSync.
type DatabaseSync struct {
	mu   sync.Mutex
	db   *sql.DB
	conn *sql.Conn
	// prepared is called with the SQL text of every statement Prepare compiles (ObservePrepare).
	prepared func(sqlText string)
}

// ObservePrepare calls observe with the SQL text of every statement Prepare compiles from now on. It stands in for a
// subclass overriding DatabaseSync.prepare, as Pi's storage tests count compilations.
// pig additive (D104): Go has no subclassing of the connection, so an observer takes the override's place.
func (database *DatabaseSync) ObservePrepare(observe func(sqlText string)) {
	database.mu.Lock()
	defer database.mu.Unlock()
	database.prepared = observe
}

// NewDatabaseSync opens path (or ":memory:") as one dedicated connection.
func NewDatabaseSync(path string, options DatabaseSyncOptions) (*DatabaseSync, error) {
	db, err := sql.Open("sqlite", dataSourceName(path, options.ReadOnly))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	database := &DatabaseSync{db: db, conn: conn}
	if _, err := conn.ExecContext(
		context.Background(), fmt.Sprintf("PRAGMA busy_timeout = %d", max(options.TimeoutMs, 0)),
	); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

// dataSourceName keeps a plain path unchanged and uses a file: URI only when the path would otherwise be read as
// driver query parameters or when read-only mode is requested.
func dataSourceName(path string, readOnly bool) string {
	if !readOnly && !strings.ContainsAny(path, "?#") {
		return path
	}
	location := url.URL{Scheme: "file", Opaque: (&url.URL{Path: filepath.ToSlash(path)}).EscapedPath()}
	if readOnly {
		location.RawQuery = "mode=ro"
	}
	return location.String()
}

func (database *DatabaseSync) connection() (*sql.Conn, error) {
	database.mu.Lock()
	defer database.mu.Unlock()
	if database.conn == nil {
		return nil, errDatabaseNotOpen
	}
	return database.conn, nil
}

// Exec runs SQL text without bindings; it may contain several statements.
func (database *DatabaseSync) Exec(sqlText string) error {
	conn, err := database.connection()
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(context.Background(), sqlText)
	return err
}

// Prepare compiles one statement.
func (database *DatabaseSync) Prepare(sqlText string) (*StatementSync, error) {
	conn, err := database.connection()
	if err != nil {
		return nil, err
	}
	database.mu.Lock()
	observe := database.prepared
	database.mu.Unlock()
	if observe != nil {
		observe(sqlText)
	}
	statement, err := conn.PrepareContext(context.Background(), sqlText)
	if err != nil {
		return nil, err
	}
	return &StatementSync{database: database, statement: statement}, nil
}

// Close closes the connection; later operations fail.
func (database *DatabaseSync) Close() error {
	database.mu.Lock()
	conn := database.conn
	database.conn = nil
	database.mu.Unlock()
	if conn == nil {
		return errDatabaseNotOpen
	}
	return errors.Join(conn.Close(), database.db.Close())
}

// IsTransaction reports whether a transaction is open on the connection (node:sqlite's DatabaseSync.isTransaction,
// sqlite3_get_autocommit() == 0). The driver exposes no autocommit query, so it probes with BEGIN, which SQLite refuses
// inside a transaction (a transaction begun by BEGIN or SAVEPOINT alike), and rolls the probe back.
func (database *DatabaseSync) IsTransaction() (bool, error) {
	conn, err := database.connection()
	if err != nil {
		return false, err
	}
	//portlint:allow ctxbackground node:sqlite's isTransaction is a synchronous getter with no signal to forward
	if _, err := conn.ExecContext(context.Background(), "BEGIN"); err != nil {
		//portlint:allow erroridentity the driver reports the refused BEGIN as a generic SQLITE_ERROR whose message alone names the cause; it has no sentinel or distinct code
		if strings.Contains(err.Error(), "within a transaction") {
			return true, nil
		}
		return false, err
	}
	//portlint:allow ctxbackground rolls back the probe begun above, which has no signal to forward
	_, err = conn.ExecContext(context.Background(), "ROLLBACK")
	return false, err
}

// StatementSync is one prepared statement of a DatabaseSync.
type StatementSync struct {
	database  *DatabaseSync
	statement *sql.Stmt
}

func (statement *StatementSync) open() error {
	_, err := statement.database.connection()
	return err
}

// Run executes the statement with positional bindings.
func (statement *StatementSync) Run(params ...any) error {
	if err := statement.open(); err != nil {
		return err
	}
	_, err := statement.statement.ExecContext(context.Background(), params...)
	return err
}

// EachText passes the first column of every result row to fn as it is read; the bytes are valid until fn returns.
func (statement *StatementSync) EachText(params []any, fn func(text []byte) error) (err error) {
	if err := statement.open(); err != nil {
		return err
	}
	rows, err := statement.statement.QueryContext(context.Background(), params...)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var text sql.RawBytes
		if err := rows.Scan(&text); err != nil {
			return err
		}
		if err := fn(text); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Get returns the first result row, or nil when there is none.
func (statement *StatementSync) Get(params ...any) (map[string]any, error) {
	rows, err := statement.query(params, 1)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

// All returns every result row.
func (statement *StatementSync) All(params ...any) ([]map[string]any, error) {
	return statement.query(params, -1)
}

func (statement *StatementSync) query(params []any, limit int) (result []map[string]any, err error) {
	if err := statement.open(); err != nil {
		return nil, err
	}
	rows, err := statement.statement.QueryContext(context.Background(), params...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result = []map[string]any{}
	for (limit < 0 || len(result) < limit) && rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for index := range values {
			targets[index] = &values[index]
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for index, column := range columns {
			row[column] = values[index]
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (statement *StatementSync) close() error { return statement.statement.Close() }

// connection is the native connection surface NodeSqliteDatabase drives; *DatabaseSync is its one production implementation.
type connection interface {
	Exec(sql string) error
	Prepare(sql string) (*StatementSync, error)
	IsTransaction() (bool, error)
	Close() error
}

// serialOperationQueue runs operations in call order. An operation starts immediately when nothing is running or
// waiting; otherwise it waits for everything before it. A transaction holds the queue until it settles.
type serialOperationQueue struct {
	mu      sync.Mutex
	tail    chan struct{}
	waiting int
}

func newSerialOperationQueue() *serialOperationQueue {
	ready := make(chan struct{})
	close(ready)
	return &serialOperationQueue{tail: ready}
}

// acquire takes the next turn and blocks until every earlier turn has been released.
func (queue *serialOperationQueue) acquire() (release func()) {
	queue.mu.Lock()
	previous := queue.tail
	turn := make(chan struct{})
	queue.tail = turn
	queue.waiting++
	queue.mu.Unlock()
	<-previous
	queue.mu.Lock()
	queue.waiting--
	queue.mu.Unlock()
	return func() { close(turn) }
}

// queued reports how many turns wait behind the running one.
func (queue *serialOperationQueue) queued() int {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return queue.waiting
}

// nodeSqliteExecutor executes SQL on one connection. Prepared statements are cached per connection by SQL text, so the
// database and its transaction handles share them across transactions.
type nodeSqliteExecutor struct {
	database   connection
	statements *statementCache
	// runOperation brackets one operation: the database queues it, a transaction handle checks that it is active.
	runOperation func(operation func() error) error
}

type statementCache struct {
	mu         sync.Mutex
	statements map[string]*StatementSync
}

func (executor *nodeSqliteExecutor) statement(sqlText string) (*StatementSync, error) {
	cache := executor.statements
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if statement := cache.statements[sqlText]; statement != nil {
		return statement, nil
	}
	statement, err := executor.database.Prepare(sqlText)
	if err != nil {
		return nil, err
	}
	cache.statements[sqlText] = statement
	return statement, nil
}

func (executor *nodeSqliteExecutor) Exec(sqlText string) error {
	return executor.runOperation(func() error { return executor.database.Exec(sqlText) })
}

func (executor *nodeSqliteExecutor) Run(sqlText string, params ...chordsqlite.SqliteValue) error {
	return executor.runOperation(func() error {
		statement, err := executor.statement(sqlText)
		if err != nil {
			return err
		}
		return statement.Run(params...)
	})
}

// EachText runs a query whose first column is text and passes each value to fn without building result rows.
func (executor *nodeSqliteExecutor) EachText(sqlText string, params []chordsqlite.SqliteValue, fn func(text []byte) error) error {
	return executor.runOperation(func() error {
		statement, err := executor.statement(sqlText)
		if err != nil {
			return err
		}
		return statement.EachText(params, fn)
	})
}

func (executor *nodeSqliteExecutor) Get(
	sqlText string, params ...chordsqlite.SqliteValue,
) (row chordsqlite.SqliteRow, err error) {
	err = executor.runOperation(func() error {
		statement, prepareErr := executor.statement(sqlText)
		if prepareErr != nil {
			return prepareErr
		}
		row, prepareErr = statement.Get(params...)
		return prepareErr
	})
	return row, err
}

func (executor *nodeSqliteExecutor) All(
	sqlText string, params ...chordsqlite.SqliteValue,
) (rows []chordsqlite.SqliteRow, err error) {
	err = executor.runOperation(func() error {
		statement, prepareErr := executor.statement(sqlText)
		if prepareErr != nil {
			return prepareErr
		}
		rows, prepareErr = statement.All(params...)
		return prepareErr
	})
	return rows, err
}

var errTransactionInactive = errors.New("SQLite transaction handle is no longer active")

// AggregateError reports a transaction callback failure together with the rollback failure that followed it. It
// deliberately does not unwrap: the callback error no longer implies that the transaction rolled back.
type AggregateError struct {
	Errors  []error
	Message string
}

func (aggregate *AggregateError) Error() string { return aggregate.Message }

// NodeSqliteDatabase is the SqliteDatabase adapter backed by a native connection.
type NodeSqliteDatabase struct {
	nodeSqliteExecutor
	access *serialOperationQueue
	closed bool
}

// NewNodeSqliteDatabase adapts database; the adapter owns it from now on. It is Pi's `new NodeSqliteDatabase(database: DatabaseSync)`.
func NewNodeSqliteDatabase(database *DatabaseSync) *NodeSqliteDatabase {
	return newNodeSqliteDatabase(database)
}

// newNodeSqliteDatabase adapts any native connection, so a test can wrap one to observe or fail its calls.
func newNodeSqliteDatabase(database connection) *NodeSqliteDatabase {
	adapter := &NodeSqliteDatabase{access: newSerialOperationQueue()}
	adapter.nodeSqliteExecutor = nodeSqliteExecutor{
		database:     database,
		statements:   &statementCache{statements: map[string]*StatementSync{}},
		runOperation: adapter.run,
	}
	return adapter
}

func (adapter *NodeSqliteDatabase) run(operation func() error) error {
	release := adapter.access.acquire()
	defer release()
	return operation()
}

// scopedExecutor is the handle passed to a transaction or exclusive operation: it runs SQL directly and answers inactive
// once its scope ends (NodeSqliteScopedExecutor). The handle's closure captures the active flag alone, so a handle costs what it
// did before exclusive operations existed; inactive is the package error of the scope's kind, never captured.
func (adapter *NodeSqliteDatabase) scopedExecutor(active *atomic.Bool, exclusive bool) *nodeSqliteExecutor {
	executor := &nodeSqliteExecutor{database: adapter.database, statements: adapter.statements}
	if exclusive {
		executor.runOperation = func(operation func() error) error {
			if !active.Load() {
				return errExclusiveInactive
			}
			return operation()
		}
		return executor
	}
	executor.runOperation = func(operation func() error) error {
		if !active.Load() {
			return errTransactionInactive
		}
		return operation()
	}
	return executor
}

// Transaction runs callback inside BEGIN IMMEDIATE ... COMMIT and rolls back when it fails or panics.
func (adapter *NodeSqliteDatabase) Transaction(
	callback func(transaction chordsqlite.SqliteExecutor) error,
) (err error) {
	release := adapter.access.acquire()
	defer release()
	if err := adapter.database.Exec("BEGIN IMMEDIATE"); err != nil {
		return err
	}
	var active atomic.Bool
	active.Store(true)
	handle := adapter.scopedExecutor(&active, false)
	settled := false
	defer func() {
		if settled {
			return
		}
		// The callback panicked: roll back before the panic continues.
		active.Store(false)
		_ = adapter.database.Exec("ROLLBACK")
	}()
	err = callback(handle)
	active.Store(false)
	if err == nil {
		err = adapter.database.Exec("COMMIT")
	}
	settled = true
	if err == nil {
		return nil
	}
	if rollbackErr := adapter.database.Exec("ROLLBACK"); rollbackErr != nil {
		return &AggregateError{
			Errors:  []error{err, rollbackErr},
			Message: "SQLite transaction failed and rollback failed",
		}
	}
	return err
}

var errExclusiveInactive = errors.New("SQLite exclusive handle is no longer active")

// exclusiveLeftTransactionOpen reports an exclusive operation that ended with a transaction still open. cause is the
// operation's own error when it failed.
type exclusiveLeftTransactionOpen struct{ cause error }

func (exclusiveLeftTransactionOpen) Error() string {
	return "SQLite exclusive operation left a transaction open; it was rolled back"
}

func (err exclusiveLeftTransactionOpen) Unwrap() error { return err.cause }

// Exclusive runs operation alone on the connection, outside a transaction: it starts after every earlier operation and
// transaction has settled, and later calls wait until it settles. The handle runs SQL immediately and is invalid
// afterwards. Use it for work that must not interleave with commits but cannot run inside a transaction, such as
// `PRAGMA wal_checkpoint`, or to hold back all later operations while doing something else. As with Transaction, calling
// the database itself from inside the operation waits for the operation and never returns.
//
// An operation that begins a transaction must end it. Otherwise later statements would silently join it and never
// commit, so a transaction still open when the operation settles (or panics) is rolled back and the call fails, with the
// operation's own error as the cause if it failed.
// Ports packages/chord/src/sqlite/node.ts NodeSqliteDatabase.exclusive.
func (adapter *NodeSqliteDatabase) Exclusive(operation func(executor chordsqlite.SqliteExecutor) error) error {
	release := adapter.access.acquire()
	defer release()
	var active atomic.Bool
	active.Store(true)
	settled := false
	defer func() {
		if settled {
			return
		}
		// The operation panicked: end its scope and roll back a transaction it left open before the panic continues.
		active.Store(false)
		if open, _ := adapter.database.IsTransaction(); open {
			_ = adapter.database.Exec("ROLLBACK")
		}
	}()
	operationErr := operation(adapter.scopedExecutor(&active, true))
	active.Store(false)
	settled = true
	open, err := adapter.database.IsTransaction()
	if err != nil {
		return err
	}
	if !open {
		return operationErr
	}
	leftOpen := exclusiveLeftTransactionOpen{cause: operationErr}
	if rollbackErr := adapter.database.Exec("ROLLBACK"); rollbackErr != nil {
		return &AggregateError{
			Errors:  []error{leftOpen, rollbackErr},
			Message: "SQLite exclusive operation left a transaction open and rollback failed",
		}
	}
	return leftOpen
}

// Close truncates the WAL and closes the connection. Repeated calls do nothing.
func (adapter *NodeSqliteDatabase) Close() error {
	release := adapter.access.acquire()
	defer release()
	if adapter.closed {
		return nil
	}
	adapter.closed = true
	adapter.statements.mu.Lock()
	var errs []error
	for sqlText, statement := range adapter.statements.statements {
		errs = append(errs, statement.close())
		delete(adapter.statements.statements, sqlText)
	}
	adapter.statements.mu.Unlock()
	checkpointErr := adapter.database.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	// Pi closes in a finally block, so a close failure replaces the checkpoint failure.
	if closeErr := adapter.database.Close(); closeErr != nil {
		return closeErr
	}
	if checkpointErr != nil {
		return checkpointErr
	}
	return errors.Join(errs...)
}

// NodeSqliteDatabaseOptions configures a file-backed SQLite connection. A nil field selects its default.
type NodeSqliteDatabaseOptions struct {
	// WalAutoCheckpointPages is the SQLite WAL auto-checkpoint threshold. SQLite and this adapter default to 1,000
	// pages; 0 disables it.
	WalAutoCheckpointPages *int
	// BusyTimeoutMs is how long SQLite waits for a competing file lock. SQLite defaults to 0; this adapter defaults to
	// 5,000 ms.
	BusyTimeoutMs *int
}

const (
	defaultWalAutoCheckpointPages = 1_000
	defaultBusyTimeoutMs          = 5_000
)

// OpenNodeSqliteDatabase opens and configures a native SQLite database facade, creating the parent directory.
func OpenNodeSqliteDatabase(path string, options NodeSqliteDatabaseOptions) (*NodeSqliteDatabase, error) {
	checkpointPages := defaultWalAutoCheckpointPages
	if options.WalAutoCheckpointPages != nil {
		checkpointPages = *options.WalAutoCheckpointPages
	}
	timeout := defaultBusyTimeoutMs
	if options.BusyTimeoutMs != nil {
		timeout = *options.BusyTimeoutMs
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
			return nil, err
		}
	}
	database, err := NewDatabaseSync(path, DatabaseSyncOptions{TimeoutMs: timeout})
	if err != nil {
		return nil, err
	}
	adapter := NewNodeSqliteDatabase(database)
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		fmt.Sprintf("PRAGMA wal_autocheckpoint = %d", checkpointPages),
	} {
		if err := adapter.Exec(pragma); err != nil {
			// Preserve the configuration failure.
			_ = adapter.Close()
			return nil, err
		}
	}
	return adapter, nil
}
