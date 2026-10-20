package replication

// Ports packages/chord/test/sqlite/replication-node.test.ts (Pi 48c8f322).

import (
	"encoding/binary"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	chordsqlite "github.com/MichaelKinsy/PiG/chord/sqlite"
)

const (
	walHeaderBytes      = 32
	walFrameHeaderBytes = 24
)

// walCommitFrames counts the committed frames of a WAL file from its header, without validating checksums.
func walCommitFrames(t *testing.T, path string) int {
	t.Helper()
	wal, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(wal) < walHeaderBytes {
		return 0
	}
	pageSize := int(binary.BigEndian.Uint32(wal[8:]))
	commits := 0
	for offset := walHeaderBytes; offset+walFrameHeaderBytes+pageSize <= len(wal); offset += walFrameHeaderBytes + pageSize {
		if binary.BigEndian.Uint32(wal[offset+4:]) != 0 {
			commits++
		}
	}
	return commits
}

type recordingHooks struct {
	mu          sync.Mutex
	operations  int
	beforeClose func(executor chordsqlite.SqliteExecutor) error
}

func (hooks *recordingHooks) AfterOperation() {
	hooks.mu.Lock()
	defer hooks.mu.Unlock()
	hooks.operations++
}

func (hooks *recordingHooks) count() int {
	hooks.mu.Lock()
	defer hooks.mu.Unlock()
	return hooks.operations
}

func (hooks *recordingHooks) BeforeClose(executor chordsqlite.SqliteExecutor) error {
	if hooks.beforeClose == nil {
		return nil
	}
	return hooks.beforeClose(executor)
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func openReplicated(t *testing.T, path string, hooks ReplicationHooks) *ReplicatedNodeSqliteDatabase {
	t.Helper()
	database, err := OpenReplicatedNodeSqliteDatabase(path, hooks, ReplicatedNodeSqliteDatabaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func expectError(t *testing.T, err error, message string) {
	t.Helper()
	if err == nil || err.Error() != message {
		t.Fatalf("error = %v, want %q", err, message)
	}
}

// waitUntil polls condition until it holds; the operations it waits for are goroutines already blocked on the connection.
func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !condition(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (database *ReplicatedNodeSqliteDatabase) inflightOperations() int {
	database.mu.Lock()
	defer database.mu.Unlock()
	return database.inflight
}

func TestReplicatedNodeSqliteRefusesDatabasesWithoutAWALFile(t *testing.T) {
	for _, target := range []string{"", ":memory:", "file::memory:"} {
		_, err := OpenReplicatedNodeSqliteDatabase(target, &recordingHooks{}, ReplicatedNodeSqliteDatabaseOptions{})
		if err == nil || err.Error() != "Replication needs a database file path, got "+jsonString(target) {
			t.Errorf("open %q error = %v", target, err)
		}
	}
}

func TestReplicatedNodeSqliteOpensInWALModeAndNeverCheckpointsOnItsOwn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replica.sqlite")
	database := openReplicated(t, path, &recordingHooks{})
	if database.Path != path {
		t.Fatalf("Path = %q, want %q", database.Path, path)
	}
	row, err := database.Get("PRAGMA journal_mode")
	mustDo(t, err)
	if row["journal_mode"] != "wal" {
		t.Fatalf("journal_mode = %v", row)
	}
	row, err = database.Get("PRAGMA wal_autocheckpoint")
	mustDo(t, err)
	if row["wal_autocheckpoint"] != int64(0) {
		t.Fatalf("wal_autocheckpoint = %v", row)
	}
	mustDo(t, database.Exec("CREATE TABLE pages (value BLOB)"))
	// SQLite's default would checkpoint once the WAL passes 1,000 pages; write well past that.
	for range 12 {
		mustDo(t, database.Transaction(func(transaction chordsqlite.SqliteExecutor) error {
			for range 100 {
				if err := transaction.Run("INSERT INTO pages (value) VALUES (randomblob(4000))"); err != nil {
					return err
				}
			}
			return nil
		}))
	}
	if frames := walCommitFrames(t, path+"-wal"); frames != 13 {
		t.Errorf("WAL commit frames = %d, want 13", frames)
	}
	info, err := os.Stat(path + "-wal")
	mustDo(t, err)
	if info.Size() <= 1_200*4_096 {
		t.Errorf("WAL size = %d, want more than %d", info.Size(), 1_200*4_096)
	}
	mustDo(t, database.Close())
}

func TestReplicatedNodeSqliteReportsEveryOperationIncludingFailedOnesAndStatementsOutsideTransactions(t *testing.T) {
	hooks := &recordingHooks{}
	database := openReplicated(t, filepath.Join(t.TempDir(), "replica.sqlite"), hooks)
	mustDo(t, database.Exec("CREATE TABLE notes (value TEXT NOT NULL)"))
	mustDo(t, database.Run("INSERT INTO notes (value) VALUES (?)", "autocommit"))
	_, err := database.Get("SELECT value FROM notes")
	mustDo(t, err)
	_, err = database.All("SELECT value FROM notes")
	mustDo(t, err)
	mustDo(t, database.Transaction(func(transaction chordsqlite.SqliteExecutor) error {
		return transaction.Run("INSERT INTO notes (value) VALUES (?)", "transaction")
	}))
	if err := database.Run("INSERT INTO notes (value) VALUES (NULL)"); err == nil {
		t.Fatal("a NOT NULL violation succeeded")
	}
	if got := hooks.count(); got != 6 {
		t.Fatalf("operations = %d, want 6", got)
	}
	mustDo(t, database.Exclusive(func(executor chordsqlite.SqliteExecutor) error {
		return executor.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	}))
	if got := hooks.count(); got != 6 {
		t.Fatalf("operations after an exclusive operation = %d, want 6", got)
	}
	mustDo(t, database.Close())
}

func TestReplicatedNodeSqliteRunsTheCloseHookAfterEarlierOperationsWhileTheWALHoldsEveryCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replica.sqlite")
	framesAtClose := -1
	var rowsAtClose chordsqlite.SqliteRow
	hooks := &recordingHooks{beforeClose: func(executor chordsqlite.SqliteExecutor) error {
		framesAtClose = walCommitFrames(t, path+"-wal")
		var err error
		rowsAtClose, err = executor.Get("SELECT count(*) AS count FROM notes")
		return err
	}}
	database := openReplicated(t, path, hooks)
	mustDo(t, database.Exec("CREATE TABLE notes (value TEXT)"))

	// Called before close and still queued behind a held connection when close is called: these must commit before the hook runs.
	release := make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- database.Exclusive(func(chordsqlite.SqliteExecutor) error {
			<-release
			return nil
		})
	}()
	waitUntil(t, "the exclusive operation to begin", func() bool { return database.inflightOperations() == 1 })
	pending := make(chan error, 3)
	for _, value := range []string{"1", "2", "3"} {
		go func() { pending <- database.Run("INSERT INTO notes (value) VALUES (?)", value) }()
	}
	waitUntil(t, "the inserts to queue", func() bool { return database.inflightOperations() == 4 })
	closed := make(chan error, 1)
	go func() { closed <- database.Close() }()
	waitUntil(t, "close to start", func() bool {
		database.mu.Lock()
		defer database.mu.Unlock()
		return database.closing
	})
	// Operations called after close fail at once, without waiting for the close to finish.
	expectError(t, database.Run("INSERT INTO notes (value) VALUES (?)", "late"), "Replicated SQLite database is closed")
	expectError(t, database.Exclusive(func(chordsqlite.SqliteExecutor) error { return nil }), "Replicated SQLite database is closed")

	close(release)
	mustDo(t, <-held)
	for range 3 {
		mustDo(t, <-pending)
	}
	mustDo(t, <-closed)
	mustDo(t, database.Close())

	if framesAtClose != 4 {
		t.Errorf("WAL commit frames at close = %d, want 4", framesAtClose)
	}
	if rowsAtClose["count"] != int64(3) {
		t.Errorf("rows at close = %v, want 3", rowsAtClose)
	}
	// SQLite checkpoints and removes the WAL when the last connection closes.
	if _, err := os.Stat(path + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("WAL after close: %v, want it removed", err)
	}
}

func TestReplicatedNodeSqliteClosesTheConnectionEvenWhenTheCloseHookFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replica.sqlite")
	database := openReplicated(t, path, &recordingHooks{beforeClose: func(chordsqlite.SqliteExecutor) error {
		return errors.New("spool failed")
	}})
	mustDo(t, database.Exec("CREATE TABLE notes (value TEXT)"))
	expectError(t, database.Close(), "spool failed")
	expectError(t, database.Close(), "spool failed")
	if _, err := os.Stat(path + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("WAL after a failed hook: %v, want it removed", err)
	}

	reopened := openReplicated(t, path, &recordingHooks{})
	rows, err := reopened.All("SELECT value FROM notes")
	mustDo(t, err)
	if len(rows) != 0 {
		t.Errorf("rows = %v, want none", rows)
	}
	mustDo(t, reopened.Close())
}

// "can only be created by opening a file" (replication-node.test.ts): a constructor would accept connections with automatic checkpoints on,
// or ones the caller still writes through, so the package's only exported function is the opener and the database type has no exported
// field besides Path.
func TestReplicatedNodeSqliteCanOnlyBeCreatedByOpeningAFile(t *testing.T) {
	files, err := parser.ParseDir(token.NewFileSet(), ".", func(info os.FileInfo) bool { return !strings.HasSuffix(info.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	var functions []string
	for _, pkg := range files {
		for _, file := range pkg.Files {
			for _, declaration := range file.Decls {
				switch declaration := declaration.(type) {
				case *ast.FuncDecl:
					if declaration.Recv == nil && declaration.Name.IsExported() {
						functions = append(functions, declaration.Name.Name)
					}
				case *ast.GenDecl:
					for _, spec := range declaration.Specs {
						typeSpec, ok := spec.(*ast.TypeSpec)
						if !ok || typeSpec.Name.Name != "ReplicatedNodeSqliteDatabase" {
							continue
						}
						structType, isStruct := typeSpec.Type.(*ast.StructType)
						if !isStruct {
							continue
						}
						for _, field := range structType.Fields.List {
							for _, name := range field.Names {
								if name.IsExported() && name.Name != "Path" {
									t.Errorf("exported field %s lets a caller build or reach the connection", name.Name)
								}
							}
						}
					}
				}
			}
		}
	}
	if want := []string{"OpenReplicatedNodeSqliteDatabase"}; !reflect.DeepEqual(functions, want) {
		t.Errorf("exported package functions = %v, want %v", functions, want)
	}
}

// A zero value has no connection the engine controls: every call fails with an error instead of panicking or reaching a connection.
func TestReplicatedNodeSqliteZeroValueRefusesEveryCall(t *testing.T) {
	const want = "Replicated SQLite database was not opened with OpenReplicatedNodeSqliteDatabase"
	var database ReplicatedNodeSqliteDatabase
	expectError(t, database.Exec("SELECT 1"), want)
	expectError(t, database.Run("SELECT 1"), want)
	_, err := database.Get("SELECT 1")
	expectError(t, err, want)
	_, err = database.All("SELECT 1")
	expectError(t, err, want)
	expectError(t, database.Transaction(func(chordsqlite.SqliteExecutor) error { return nil }), want)
	expectError(t, database.Exclusive(func(chordsqlite.SqliteExecutor) error { return nil }), want)
	expectError(t, database.Close(), want)
}
