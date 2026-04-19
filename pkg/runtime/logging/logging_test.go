package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newCapture() (*bytes.Buffer, *slog.Logger) {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return &buf, slog.New(h)
}

// parseLine decodes one JSON log line into a map.
func parseLine(t *testing.T, data []byte) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to parse JSON log: %v\nraw: %s", err, data)
	}
	return m
}

// ---------------------------------------------------------------------------
// Level.String()
// ---------------------------------------------------------------------------

func TestLevelString(t *testing.T) {
	cases := []struct {
		l    Level
		want string
	}{
		{LevelDebug, "DEBUG"},
		{LevelInfo, "INFO"},
		{LevelWarn, "WARN"},
		{LevelError, "ERROR"},
		{Level(99), "Level(99)"},
	}
	for _, tc := range cases {
		if got := tc.l.String(); got != tc.want {
			t.Errorf("Level(%d).String() = %q, want %q", int(tc.l), got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Attribute constructors
// ---------------------------------------------------------------------------

func TestAttributeConstructors(t *testing.T) {
	a1 := String("host", "localhost")
	if a1.Key != "host" || a1.Value != "localhost" {
		t.Errorf("String attr: got %+v", a1)
	}

	a2 := Int("port", 5432)
	if a2.Key != "port" || a2.Value != 5432 {
		t.Errorf("Int attr: got %+v", a2)
	}

	a3 := Int64("rows", 42)
	if a3.Key != "rows" || a3.Value != int64(42) {
		t.Errorf("Int64 attr: got %+v", a3)
	}

	a4 := Duration("elapsed", 3*time.Second)
	if a4.Key != "elapsed" || a4.Value != 3*time.Second {
		t.Errorf("Duration attr: got %+v", a4)
	}

	err := fmt.Errorf("boom")
	a5 := Error(err)
	if a5.Key != "error" || a5.Value != err {
		t.Errorf("Error attr: got %+v", a5)
	}

	a6 := Bool("verbose", true)
	if a6.Key != "verbose" || a6.Value != true {
		t.Errorf("Bool attr: got %+v", a6)
	}
}

// ---------------------------------------------------------------------------
// NopLogger
// ---------------------------------------------------------------------------

func TestNopLoggerImplementsLogger(t *testing.T) {
	var l Logger = NopLogger{}
	// Must not panic.
	l.Log(context.Background(), LevelInfo, "ignored")
	l.Log(context.Background(), LevelError, "also ignored", String("k", "v"))
}

// ---------------------------------------------------------------------------
// StdLogger — basic level filtering
// ---------------------------------------------------------------------------

func TestStdLoggerLevelFiltering(t *testing.T) {
	buf, sl := newCapture()
	l := NewStdLogger(sl, WithLevel(LevelWarn))

	ctx := context.Background()
	l.Log(ctx, LevelDebug, "debug message")
	l.Log(ctx, LevelInfo, "info message")
	l.Log(ctx, LevelWarn, "warn message")
	l.Log(ctx, LevelError, "error message")

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("expected 2 log lines (warn+error), got %d: %s", len(lines), buf.String())
	}

	m1 := parseLine(t, lines[0])
	if m1["msg"] != "warn message" {
		t.Errorf("first line msg = %v, want %q", m1["msg"], "warn message")
	}
	m2 := parseLine(t, lines[1])
	if m2["msg"] != "error message" {
		t.Errorf("second line msg = %v, want %q", m2["msg"], "error message")
	}
}

// ---------------------------------------------------------------------------
// StdLogger — attributes appear in output
// ---------------------------------------------------------------------------

func TestStdLoggerAttributes(t *testing.T) {
	buf, sl := newCapture()
	l := NewStdLogger(sl)

	l.Log(context.Background(), LevelInfo, "hello", String("key", "val"), Int("n", 7))

	m := parseLine(t, bytes.TrimSpace(buf.Bytes()))
	if m["key"] != "val" {
		t.Errorf("expected key=val, got %v", m["key"])
	}
	if n, ok := m["n"].(float64); !ok || int(n) != 7 {
		t.Errorf("expected n=7, got %v", m["n"])
	}
}

// ---------------------------------------------------------------------------
// StdLogger — slow query detection
// ---------------------------------------------------------------------------

func TestStdLoggerSlowQuery(t *testing.T) {
	buf, sl := newCapture()
	l := NewStdLogger(sl, WithSlowThreshold(50*time.Millisecond))

	// Fast query — debug level.
	l.LogQuery(context.Background(), QueryEvent{
		Query:    "SELECT 1",
		Duration: 10 * time.Millisecond,
		RowCount: 1,
	})

	// Slow query — promoted to warn.
	l.LogQuery(context.Background(), QueryEvent{
		Query:    "SELECT * FROM big_table",
		Duration: 100 * time.Millisecond,
		RowCount: 50000,
	})

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("expected 2 log lines, got %d: %s", len(lines), buf.String())
	}

	fast := parseLine(t, lines[0])
	if fast["level"] != "DEBUG" {
		t.Errorf("fast query level = %v, want DEBUG", fast["level"])
	}

	slow := parseLine(t, lines[1])
	if slow["level"] != "WARN" {
		t.Errorf("slow query level = %v, want WARN", slow["level"])
	}
	if slow["slow"] != true {
		t.Errorf("slow query should have slow=true, got %v", slow["slow"])
	}
}

// ---------------------------------------------------------------------------
// StdLogger — LogQuery error
// ---------------------------------------------------------------------------

func TestStdLoggerLogQueryError(t *testing.T) {
	buf, sl := newCapture()
	l := NewStdLogger(sl)

	l.LogQuery(context.Background(), QueryEvent{
		Query:    "INSERT INTO ...",
		Duration: 5 * time.Millisecond,
		Error:    fmt.Errorf("unique violation"),
	})

	m := parseLine(t, bytes.TrimSpace(buf.Bytes()))
	if m["level"] != "ERROR" {
		t.Errorf("error query level = %v, want ERROR", m["level"])
	}
	if m["query"] != "INSERT INTO ..." {
		t.Errorf("query = %v", m["query"])
	}
}

// ---------------------------------------------------------------------------
// StdLogger — LogQuery with args
// ---------------------------------------------------------------------------

func TestStdLoggerLogQueryWithArgs(t *testing.T) {
	buf, sl := newCapture()
	l := NewStdLogger(sl, WithArgs(true))

	l.LogQuery(context.Background(), QueryEvent{
		Query:    "SELECT * FROM users WHERE id = $1",
		Args:     []any{42},
		Duration: 2 * time.Millisecond,
		RowCount: 1,
	})

	m := parseLine(t, bytes.TrimSpace(buf.Bytes()))
	args, ok := m["args"]
	if !ok {
		t.Fatal("expected args key in log output")
	}
	arr, ok := args.([]any)
	if !ok || len(arr) != 1 {
		t.Errorf("args = %v, want [42]", args)
	}
}

// ---------------------------------------------------------------------------
// StdLogger — LogTx
// ---------------------------------------------------------------------------

func TestStdLoggerLogTx(t *testing.T) {
	buf, sl := newCapture()
	l := NewStdLogger(sl)

	l.LogTx(context.Background(), TxEvent{
		Action:   "commit",
		Duration: 3 * time.Millisecond,
	})

	m := parseLine(t, bytes.TrimSpace(buf.Bytes()))
	if m["action"] != "commit" {
		t.Errorf("action = %v, want commit", m["action"])
	}
}

func TestStdLoggerLogTxError(t *testing.T) {
	buf, sl := newCapture()
	l := NewStdLogger(sl)

	l.LogTx(context.Background(), TxEvent{
		Action:   "rollback",
		Duration: 1 * time.Millisecond,
		Error:    fmt.Errorf("connection lost"),
	})

	m := parseLine(t, bytes.TrimSpace(buf.Bytes()))
	if m["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", m["level"])
	}
	if m["action"] != "rollback" {
		t.Errorf("action = %v, want rollback", m["action"])
	}
}

// ---------------------------------------------------------------------------
// Compile-time interface check
// ---------------------------------------------------------------------------

var _ Logger = NopLogger{}
var _ Logger = (*StdLogger)(nil)
