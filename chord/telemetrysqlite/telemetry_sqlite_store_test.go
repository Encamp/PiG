package telemetrysqlite

// Ports packages/chord/test/telemetry-sqlite-store.test.ts "SQLite telemetry reads".

import (
	"context"
	"errors"
	"testing"

	chordsqlite "github.com/MichaelKinsy/PiG/chord/sqlite"
	"github.com/MichaelKinsy/PiG/chord/sqlite/cloudflare"
	"github.com/MichaelKinsy/PiG/telemetry"
	"github.com/MichaelKinsy/PiG/telemetry/telemetrytest"
)

// countingExecutor counts the All calls of a transaction handle: upstream's `counted` executor.
type countingExecutor struct {
	chordsqlite.SqliteExecutor
	queries *int
}

func (executor countingExecutor) All(sql string, params ...chordsqlite.SqliteValue) ([]chordsqlite.SqliteRow, error) {
	*executor.queries++
	return executor.SqliteExecutor.All(sql, params...)
}

// Upstream: "uses two span queries for a page, not one query per trace".
func TestUsesTwoSpanQueriesForAPageNotOnePerTrace(t *testing.T) {
	f := newFixture(t, SqliteTelemetryOptions{})
	for range 25 {
		f.span("root")
	}
	f.flush()
	queries := 0
	f.db.setHook(func(callback func(chordsqlite.SqliteExecutor) error, original func(func(chordsqlite.SqliteExecutor) error) error) error {
		return original(func(tx chordsqlite.SqliteExecutor) error {
			return callback(countingExecutor{SqliteExecutor: tx, queries: &queries})
		})
	})
	for _, limit := range []int{20, 1} {
		queries = 0
		result, err := f.telemetry.FindTraces(telemetry.TraceQuery{Limit: &limit})
		check(t, err)
		if len(result.Traces) != limit {
			t.Fatalf("limit %d returned %d traces", limit, len(result.Traces))
		}
		if queries != 2 {
			t.Fatalf("limit %d ran %d span queries, want 2", limit, queries)
		}
	}
}

// Upstream: "reads committed rows without implicitly flushing, including after close".
func TestReadsCommittedRowsWithoutImplicitlyFlushingIncludingAfterClose(t *testing.T) {
	mockDate(t, 20_001)
	f := newFixture(t, SqliteTelemetryOptions{})
	f.span("pending")
	result, err := f.telemetry.FindTraces(telemetry.TraceQuery{})
	check(t, err)
	if len(result.Traces) != 0 {
		t.Fatalf("traces before flush = %v", result.Traces)
	}
	f.telemetry.Counter("partial", telemetry.InstrumentOptions{}).Add(2, nil)
	metrics, err := f.telemetry.FindMetrics(telemetry.MetricQuery{Name: "partial"})
	check(t, err)
	if len(metrics) != 0 {
		t.Fatalf("metrics before close = %v", metrics)
	}
	check(t, f.telemetry.Close())
	result, err = f.telemetry.FindTraces(telemetry.TraceQuery{})
	check(t, err)
	if len(result.Traces) != 1 {
		t.Fatalf("traces after close = %v", result.Traces)
	}
	trace, err := f.telemetry.GetTrace(result.Traces[0].TraceID)
	check(t, err)
	if trace == nil || trace.Spans[0].Name != "pending" {
		t.Fatalf("trace = %+v, want the pending span", trace)
	}
	metrics, err = f.telemetry.FindMetrics(telemetry.MetricQuery{Name: "partial"})
	check(t, err)
	if len(metrics) != 1 || metrics[0].Count != 1 || metrics[0].Sum != 2 {
		t.Fatalf("metrics after close = %+v, want count 1 sum 2", metrics)
	}
}

// Upstream: "keeps surviving descendants discoverable after a stored parent is removed".
func TestKeepsSurvivingDescendantsDiscoverableAfterAStoredParentIsRemoved(t *testing.T) {
	f := newFixture(t, SqliteTelemetryOptions{})
	check(t, f.telemetry.StartSpan(telemetry.SpanOptions{Name: "root"}, func(root telemetry.TelemetrySpan) error {
		return root.StartSpan(telemetry.SpanOptions{Name: "child"}, func(telemetry.TelemetrySpan) error { return nil })
	}))
	f.flush()
	found, err := f.telemetry.FindTraces(telemetry.TraceQuery{})
	check(t, err)
	original := found.Traces[0]
	var parent telemetry.StoredSpan
	for _, span := range original.Spans {
		if span.Name == "root" {
			parent = span
		}
	}
	check(t, f.db.Run("DELETE FROM telemetry_spans WHERE id = ?", parent.ID))
	found, err = f.telemetry.FindTraces(telemetry.TraceQuery{})
	check(t, err)
	trace := found.Traces[0]
	if trace.TraceID != original.TraceID || len(trace.Spans) != 1 {
		t.Fatalf("trace = %+v, want the original trace with one span", trace)
	}
	if trace.Spans[0].Name != "child" || trace.Spans[0].ParentID == nil || *trace.Spans[0].ParentID != parent.ID {
		t.Fatalf("span = %+v, want child with the removed parent", trace.Spans[0])
	}
	got, err := f.telemetry.GetTrace(original.TraceID)
	check(t, err)
	if got == nil || len(got.Spans) != 1 {
		t.Fatalf("getTrace = %+v, want one span", got)
	}
}

func runStoreConformance(t *testing.T, name string, factory telemetrytest.StoreFixtureFactory) {
	t.Helper()
	cases := telemetrytest.CreateTelemetryStoreConformance(name, factory)
	if len(cases) == 0 {
		t.Fatal("store conformance suite is empty")
	}
	for _, testCase := range cases {
		t.Run(testCase.Name, func(t *testing.T) {
			// upstream: packages/chord/test/telemetry-sqlite-store.test.ts:40,41 (telemetryStoreConformance)
			if err := testCase.Run(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func storeFixture(recorder *SqliteTelemetry, dispose func() error) telemetrytest.StoreFixture {
	return telemetrytest.StoreFixture{
		Context: recorder,
		Store:   recorder,
		Flush:   func(context.Context) error { return recorder.Flush() },
		Dispose: func(context.Context) error { return errors.Join(recorder.Close(), dispose()) },
	}
}

// TestSqliteTelemetryStoreConformance ports telemetryStoreConformance("SQLite telemetry store conformance", fixture).
// Pi source: packages/chord/src/telemetry-sqlite/store.ts
func TestSqliteTelemetryStoreConformance(t *testing.T) {
	runStoreConformance(t, "SQLite telemetry store conformance", func(context.Context) (telemetrytest.StoreFixture, error) {
		db, err := openMemoryDatabase()
		if err != nil {
			return telemetrytest.StoreFixture{}, err
		}
		recorder, err := CreateSqliteTelemetry(db, SqliteTelemetryOptions{})
		if err != nil {
			return telemetrytest.StoreFixture{}, errors.Join(err, db.Close())
		}
		return storeFixture(recorder, db.Close), nil
	})
}

// TestDurableObjectSqliteTelemetryStoreConformance ports telemetryStoreConformance("Durable Object SQLite telemetry
// store conformance", ...): the same suite over the Durable Object adapter.
func TestDurableObjectSqliteTelemetryStoreConformance(t *testing.T) {
	runStoreConformance(t, "Durable Object SQLite telemetry store conformance", func(context.Context) (telemetrytest.StoreFixture, error) {
		storage := newDurableObjectStorage(t)
		db := cloudflare.NewDurableObjectSqliteDatabase(storage)
		recorder, err := CreateSqliteTelemetry(db, SqliteTelemetryOptions{})
		if err != nil {
			return telemetrytest.StoreFixture{}, err
		}
		return storeFixture(recorder, db.Close), nil
	})
}
