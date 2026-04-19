package logging

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Level represents the severity of a log message.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

var levelNames = map[Level]string{
	LevelDebug: "DEBUG",
	LevelInfo:  "INFO",
	LevelWarn:  "WARN",
	LevelError: "ERROR",
}

// String returns the human-readable name of the level.
func (l Level) String() string {
	if name, ok := levelNames[l]; ok {
		return name
	}
	return fmt.Sprintf("Level(%d)", int(l))
}

// Logger is the interface for structured logging within the ORM.
type Logger interface {
	Log(ctx context.Context, level Level, msg string, attrs ...Attr)
}

// Attr is a key-value pair attached to a log message.
type Attr struct {
	Key   string
	Value any
}

// String creates a string attribute.
func String(key, value string) Attr {
	return Attr{Key: key, Value: value}
}

// Int creates an int attribute.
func Int(key string, value int) Attr {
	return Attr{Key: key, Value: value}
}

// Int64 creates an int64 attribute.
func Int64(key string, value int64) Attr {
	return Attr{Key: key, Value: value}
}

// Duration creates a duration attribute.
func Duration(key string, value time.Duration) Attr {
	return Attr{Key: key, Value: value}
}

// Error creates an error attribute with the key "error".
func Error(err error) Attr {
	return Attr{Key: "error", Value: err}
}

// Bool creates a boolean attribute.
func Bool(key string, value bool) Attr {
	return Attr{Key: key, Value: value}
}

// QueryEvent holds structured data for a database query log entry.
type QueryEvent struct {
	Query    string
	Args     []any
	Duration time.Duration
	Error    error
	RowCount int64
}

// TxEvent holds structured data for a transaction lifecycle log entry.
type TxEvent struct {
	Action   string // "begin", "commit", "rollback"
	Duration time.Duration
	Error    error
}

// NopLogger discards all log messages.
type NopLogger struct{}

// Log implements Logger and does nothing.
func (NopLogger) Log(ctx context.Context, level Level, msg string, attrs ...Attr) {}

// StdLogger wraps slog.Logger with ORM-specific behaviour such as slow-query
// detection and optional argument logging.
type StdLogger struct {
	logger        *slog.Logger
	level         Level
	slowThreshold time.Duration
	logArgs       bool
}

// StdOption configures a StdLogger.
type StdOption func(*StdLogger)

// WithLevel sets the minimum log level.
func WithLevel(l Level) StdOption {
	return func(s *StdLogger) { s.level = l }
}

// WithSlowThreshold sets the duration above which queries are logged as warnings.
func WithSlowThreshold(d time.Duration) StdOption {
	return func(s *StdLogger) { s.slowThreshold = d }
}

// WithArgs enables or disables logging of query arguments.
func WithArgs(enable bool) StdOption {
	return func(s *StdLogger) { s.logArgs = enable }
}

// NewStdLogger creates a StdLogger that delegates to the given slog.Logger.
func NewStdLogger(logger *slog.Logger, opts ...StdOption) *StdLogger {
	l := &StdLogger{
		logger:        logger,
		level:         LevelDebug,
		slowThreshold: 200 * time.Millisecond,
		logArgs:       false,
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

func levelToSlog(l Level) slog.Level {
	switch l {
	case LevelDebug:
		return slog.LevelDebug
	case LevelInfo:
		return slog.LevelInfo
	case LevelWarn:
		return slog.LevelWarn
	case LevelError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Log implements the Logger interface.
func (l *StdLogger) Log(ctx context.Context, level Level, msg string, attrs ...Attr) {
	if level < l.level {
		return
	}
	slogAttrs := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		slogAttrs = append(slogAttrs, slog.Any(a.Key, a.Value))
	}
	args := make([]any, len(slogAttrs))
	for i, a := range slogAttrs {
		args[i] = a
	}
	l.logger.LogAttrs(ctx, levelToSlog(level), msg, slogAttrs...)
}

// LogQuery logs a query event, promoting slow queries to warnings.
func (l *StdLogger) LogQuery(ctx context.Context, event QueryEvent) {
	level := LevelDebug
	if event.Error != nil {
		level = LevelError
	} else if l.slowThreshold > 0 && event.Duration >= l.slowThreshold {
		level = LevelWarn
	}
	if level < l.level {
		return
	}

	attrs := []Attr{
		String("query", event.Query),
		Duration("duration", event.Duration),
		Int64("rows", event.RowCount),
	}
	if l.logArgs {
		attrs = append(attrs, Attr{Key: "args", Value: event.Args})
	}
	if event.Error != nil {
		attrs = append(attrs, Error(event.Error))
	}
	if l.slowThreshold > 0 && event.Duration >= l.slowThreshold {
		attrs = append(attrs, Bool("slow", true))
	}

	l.Log(ctx, level, "query", attrs...)
}

// LogTx logs a transaction lifecycle event.
func (l *StdLogger) LogTx(ctx context.Context, event TxEvent) {
	level := LevelDebug
	if event.Error != nil {
		level = LevelError
	}
	if level < l.level {
		return
	}

	attrs := []Attr{
		String("action", event.Action),
		Duration("duration", event.Duration),
	}
	if event.Error != nil {
		attrs = append(attrs, Error(event.Error))
	}

	l.Log(ctx, level, "transaction", attrs...)
}
