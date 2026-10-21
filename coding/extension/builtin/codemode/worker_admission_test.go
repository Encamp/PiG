package codemode

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
)

// Ports packages/coding-agent/test/codemode-worker-admission.test.ts.
//
// Pi's test keeps workers alive by replacing Worker.prototype.terminate and lets one actual exit through. A Go VM exits on its own, so
// holdWorkers reserves slots the way a worker that has not exited holds one, and its releases are the exits.

func runCode(ctx context.Context, t *testing.T, code string) agent.AgentToolResult {
	t.Helper()
	params, _ := json.Marshal(map[string]string{"code": code})
	result, err := Execute(ctx, "worker-admission", params, nil, Options{})
	if err != nil {
		t.Error(err)
	}
	return result
}

// startRun runs code on its own goroutine, as the test's `run(...)` without an await.
func startRun(ctx context.Context, t *testing.T, code string) <-chan agent.AgentToolResult {
	done := make(chan agent.AgentToolResult, 1)
	go func() { done <- runCode(ctx, t, code) }()
	return done
}

func resultText(result agent.AgentToolResult) string {
	var b strings.Builder
	for _, item := range result.Content {
		if text, ok := item.(ai.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

func activeWorkers() int {
	codemodeWorkers.mu.Lock()
	defer codemodeWorkers.mu.Unlock()
	return codemodeWorkers.active
}

func waitForActiveWorkers(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for activeWorkers() != want {
		if time.Now().After(deadline) {
			t.Fatalf("%d codemode workers alive, want %d", activeWorkers(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitIdle returns once every VM has exited and given its slot back.
func (a *workerAdmission) waitIdle() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for a.active > 0 {
		a.idle.Wait()
	}
}

// holdWorkers reserves n worker slots and returns one release per slot, in reservation order. Each release is idempotent and a
// cleanup runs the ones the test left.
func holdWorkers(t *testing.T, n int) []func() {
	t.Helper()
	waitForActiveWorkers(t, 0)
	releases := make([]func(), n)
	for i := range n {
		release, outcome := codemodeWorkers.reserve(t.Context(), time.Second)
		if outcome != workerReserved {
			t.Fatalf("reserve %d = %v, want reserved", i, outcome)
		}
		var once sync.Once
		releases[i] = func() { once.Do(release) }
	}
	t.Cleanup(func() {
		for _, release := range releases {
			release()
		}
	})
	return releases
}

func receiveWithin[T any](t *testing.T, ch <-chan T, d time.Duration, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(d):
		t.Fatalf("%s did not settle within %v", what, d)
		panic("unreachable")
	}
}

func assertPending[T any](t *testing.T, ch <-chan T, what string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("%s settled while every worker slot was held", what)
	case <-time.After(50 * time.Millisecond):
	}
}

// Workers still running or shutting down hold their slots across calls: at the limit a script waits for a worker instead of
// failing (#11184), an abort or a deadline while it waits fails it without running it, and freeing one slot admits exactly the
// waiting script.
func TestCodemodeWorkerAdmissionBoundsSurvivingWorkers(t *testing.T) {
	releases := holdWorkers(t, 2)

	waiting := startRun(t.Context(), t, "return 3")

	abortWhileWaiting, abort := context.WithCancel(t.Context())
	aborting := startRun(abortWhileWaiting, t, "while (true) {}")
	time.Sleep(50 * time.Millisecond)
	abort()
	aborted := receiveWithin(t, aborting, 5*time.Second, "the aborted wait")
	if !aborted.IsError {
		t.Fatalf("aborted run succeeded: %s", resultText(aborted))
	}
	if details, ok := aborted.Details.(ToolDetails); !ok || len(details.Calls) != 0 {
		t.Fatalf("details = %#v, want no calls", aborted.Details)
	}
	if text := resultText(aborted); !regexp.MustCompile(`(?s)Script aborted:.*The script did not start\.`).MatchString(text) {
		t.Fatalf("aborted text = %q", text)
	}

	timedOut := runCode(t.Context(), t, "// @options: {\"timeout_ms\": 50}\nreturn 1")
	if !timedOut.IsError {
		t.Fatalf("timed-out run succeeded: %s", resultText(timedOut))
	}
	if text := resultText(timedOut); !strings.Contains(text, "Script timed out: Execution timed out after 50 ms while waiting for a free codemode worker") || !strings.HasSuffix(text, "\n\nThe script did not start.") {
		t.Fatalf("timed-out text = %q", text)
	}

	assertPending(t, waiting, "the waiting script")
	if got := activeWorkers(); got != 2 {
		t.Fatalf("%d workers held, want 2", got)
	}

	// Releasing one actual worker admits exactly the waiting script.
	releases[0]()
	admitted := receiveWithin(t, waiting, 10*time.Second, "the admitted script")
	if admitted.IsError {
		t.Fatalf("admitted script failed: %s", resultText(admitted))
	}
	if got := resultText(admitted); !strings.HasSuffix(got, "3") {
		t.Fatalf("admitted text = %q, want a trailing 3", got)
	}
	waitForActiveWorkers(t, 1)
}

// Every end of a script's worker gives its slot back: the success and the deadline here, the abort in the next test.
func TestCodemodeWorkerSlotIsReturnedAfterSuccessAndTimeout(t *testing.T) {
	waitForActiveWorkers(t, 0)
	if result := runCode(t.Context(), t, "return 1"); result.IsError {
		t.Fatalf("success run failed: %s", resultText(result))
	}
	waitForActiveWorkers(t, 0)
	result := runCode(t.Context(), t, "// @options: {\"timeout_ms\": 200}\nwhile (true) {}")
	if text := resultText(result); !result.IsError || !strings.Contains(text, "Script timed out:") {
		t.Fatalf("timeout run = %q (error %v)", text, result.IsError)
	}
	waitForActiveWorkers(t, 0)
}

// A cancelled VM keeps its slot until it exits, and gives it back when it does.
func TestCodemodeWorkerHoldsItsSlotWhileRunning(t *testing.T) {
	waitForActiveWorkers(t, 0)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan agent.AgentToolResult, 1)
	go func() { done <- runCode(ctx, t, "while (true) {}") }()
	waitForActiveWorkers(t, 1)
	cancel()
	if result := <-done; !result.IsError || !strings.Contains(resultText(result), "Script aborted:") {
		t.Fatalf("result = %q", resultText(result))
	}
	waitForActiveWorkers(t, 0)
}

// A script that was already aborted when it asks for a worker fails without taking a slot or waiting.
func TestCodemodeWorkerAdmissionRefusesAnAbortedCallerAtOnce(t *testing.T) {
	releases := holdWorkers(t, 2)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	aborted := runCode(ctx, t, "return 1")
	if text := resultText(aborted); !aborted.IsError || !strings.Contains(text, "Script aborted:") || !strings.Contains(text, "The script did not start.") {
		t.Fatalf("aborted run = %q (error %v)", text, aborted.IsError)
	}
	releases[0]()
	releases[1]()
	waitForActiveWorkers(t, 0)
}

// #10748, #11184: a burst runs two workers at a time and queues the rest in arrival order.
func TestCodemodeWorkerAdmissionQueuesABurstInArrivalOrder(t *testing.T) {
	waitForActiveWorkers(t, 0)
	type admission struct {
		index   int
		release func()
	}
	granted := make(chan admission, 6)
	for i := range 6 {
		started := make(chan struct{})
		go func() {
			close(started)
			release, outcome := codemodeWorkers.reserve(t.Context(), 30*time.Second)
			if outcome != workerReserved {
				t.Errorf("reserve %d = %v", i, outcome)
				return
			}
			granted <- admission{i, release}
		}()
		<-started
		// The goroutine enters the queue before the next one is started.
		deadline := time.Now().Add(5 * time.Second)
		for {
			codemodeWorkers.mu.Lock()
			queued := len(codemodeWorkers.waiters) + codemodeWorkers.active
			codemodeWorkers.mu.Unlock()
			if queued == i+1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("reservation %d never queued", i)
			}
			time.Sleep(time.Millisecond)
		}
	}
	var held []admission
	for released := range 4 {
		for len(held) < released+2 {
			held = append(held, receiveWithin(t, granted, 5*time.Second, "an admission"))
		}
		assertPending(t, granted, "the next reservation")
		if got := activeWorkers(); got != 2 {
			t.Fatalf("%d workers held, want 2", got)
		}
		held[released].release()
	}
	for len(held) < 6 {
		held = append(held, receiveWithin(t, granted, 5*time.Second, "an admission"))
	}
	for i, a := range held {
		if a.index != i {
			t.Fatalf("admission %d went to reservation %d; want arrival order", i, a.index)
		}
		// The first four were released while the rest waited.
		if i >= 4 {
			a.release()
		}
	}
	waitForActiveWorkers(t, 0)
}

// #11184: in-process hosts with many sessions can raise the limit with PI_CODEMODE_MAX_WORKERS.
func TestCodemodeWorkerAdmissionReadsTheLimitFromPICodemodeMaxWorkers(t *testing.T) {
	t.Setenv("PI_CODEMODE_MAX_WORKERS", "3")
	releases := holdWorkers(t, 3)
	fourth := make(chan func(), 1)
	go func() {
		release, outcome := codemodeWorkers.reserve(t.Context(), 30*time.Second)
		if outcome == workerReserved {
			fourth <- release
		}
	}()
	assertPending(t, fourth, "the fourth reservation")
	if got := activeWorkers(); got != 3 {
		t.Fatalf("%d workers held, want 3", got)
	}
	releases[0]()
	receiveWithin(t, fourth, 5*time.Second, "the fourth reservation")()
}

func TestCodemodeWorkerAdmissionIgnoresAnInvalidPICodemodeMaxWorkers(t *testing.T) {
	t.Setenv("PI_CODEMODE_MAX_WORKERS", "0")
	releases := holdWorkers(t, 2)
	third := make(chan func(), 1)
	go func() {
		release, outcome := codemodeWorkers.reserve(t.Context(), 30*time.Second)
		if outcome == workerReserved {
			third <- release
		}
	}()
	assertPending(t, third, "the third reservation")
	releases[0]()
	receiveWithin(t, third, 5*time.Second, "the third reservation")()
}

// `Number.isInteger(Number(value)) && value > 0`: JavaScript's number syntax picks the limit and anything else keeps the default 2.
func TestMaxCodemodeWorkersFollowsNumberOfTheEnvironmentValue(t *testing.T) {
	for _, tc := range []struct {
		value string
		set   bool
		want  float64
	}{
		{"", false, 2},
		{"", true, 2},
		{"0", true, 2},
		{"-1", true, 2},
		{"2.5", true, 2},
		{"abc", true, 2},
		{"NaN", true, 2},
		{"Infinity", true, 2},
		{"1", true, 1},
		{"3", true, 3},
		{" 4 ", true, 4},
		{"5.0", true, 5},
		{"0x6", true, 6},
		{"1e1", true, 10},
	} {
		if tc.set {
			t.Setenv("PI_CODEMODE_MAX_WORKERS", tc.value)
		}
		if got := maxCodemodeWorkers(); got != tc.want {
			t.Errorf("PI_CODEMODE_MAX_WORKERS=%q (set %v): limit %v, want %v", tc.value, tc.set, got, tc.want)
		}
	}
}

// A waiter whose abort or deadline came first leaves the queue and takes no slot; the slot goes to the next waiter.
func TestCodemodeWorkerAdmissionDropsAbortedAndTimedOutWaiters(t *testing.T) {
	releases := holdWorkers(t, 2)
	ctx, cancel := context.WithCancel(t.Context())
	aborted := make(chan workerOutcome, 1)
	go func() { _, outcome := codemodeWorkers.reserve(ctx, 30*time.Second); aborted <- outcome }()
	timedOut := make(chan workerOutcome, 1)
	go func() { _, outcome := codemodeWorkers.reserve(t.Context(), 20*time.Millisecond); timedOut <- outcome }()
	if got := receiveWithin(t, timedOut, 5*time.Second, "the timed-out waiter"); got != workerTimedOut {
		t.Fatalf("timed-out waiter = %v, want timeout", got)
	}
	cancel()
	if got := receiveWithin(t, aborted, 5*time.Second, "the aborted waiter"); got != workerAborted {
		t.Fatalf("aborted waiter = %v, want aborted", got)
	}
	next := make(chan func(), 1)
	go func() {
		release, outcome := codemodeWorkers.reserve(t.Context(), 30*time.Second)
		if outcome == workerReserved {
			next <- release
		}
	}()
	assertPending(t, next, "the next waiter")
	releases[0]()
	receiveWithin(t, next, 5*time.Second, "the next waiter")()
	releases[1]()
	waitForActiveWorkers(t, 0)
}

// The reservation covers the one worker a script starts. A launch that resumes after the result settled finds it released and starts
// no worker, and the slot goes back exactly once whichever way the script ends. (Pi: "starts no worker for a launch that resumes
// after its result timed out"; the sandbox's own launch guard is tested in the codemode package.)
func TestWorkerReservationReleasesExactlyOnce(t *testing.T) {
	waitForActiveWorkers(t, 0)
	newReservation := func() *workerReservation {
		release, outcome := codemodeWorkers.reserve(t.Context(), time.Second)
		if outcome != workerReserved {
			t.Fatalf("reserve = %v", outcome)
		}
		return newWorkerReservation(release)
	}

	// Handed to the worker: the worker's exit releases the slot, and the script's release after it does not.
	r := newReservation()
	exit, err := r.acquireWorker()
	if err != nil {
		t.Fatal(err)
	}
	r.releaseUnused()
	if got := activeWorkers(); got != 1 {
		t.Fatalf("%d slots after the script's release, want the worker's 1", got)
	}
	exit()
	exit()
	waitForActiveWorkers(t, 0)

	// Never handed over: the script's release frees the slot, and a launch resuming after it starts no worker.
	r = newReservation()
	r.releaseUnused()
	r.releaseUnused()
	waitForActiveWorkers(t, 0)
	if _, err := r.acquireWorker(); err == nil || err.Error() != "The script already settled" {
		t.Fatalf("late acquireWorker error = %v, want \"The script already settled\"", err)
	}
	waitForActiveWorkers(t, 0)

	// A second worker request takes no second slot.
	r = newReservation()
	exit, err = r.acquireWorker()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.acquireWorker(); err == nil {
		t.Fatal("a second acquireWorker succeeded")
	}
	exit()
	waitForActiveWorkers(t, 0)
}
