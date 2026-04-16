package logging

import (
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// Setup configures the global slog logger with dual output (stderr + rotating
// log file). logsDir is the directory where log files are written. binaryName
// is used as the log filename (e.g. "control-plane" → "control-plane.log").
//
// If logsDir is empty or the directory cannot be created, Setup falls back to
// stderr-only logging and returns a no-op cleanup function.
//
// The returned cleanup function closes the log file and must be deferred.
// Setup also installs a SIGHUP handler that reopens the log file, allowing
// external log rotation tools (e.g. logrotate) to rotate files in place.
func Setup(logsDir, binaryName string) func() {
	level := parseLevel(os.Getenv("LOG_LEVEL"))

	if logsDir == "" {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: level,
		})))
		return func() {}
	}

	if err := os.MkdirAll(logsDir, 0750); err != nil {
		// Fall back to stderr-only; log the error so operators notice.
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: level,
		})))
		slog.Warn("file logging unavailable: failed to create log directory", "dir", logsDir, "error", err)
		return func() {}
	}

	logPath := filepath.Join(logsDir, binaryName+".log")
	rf, err := newReopenableFile(logPath)
	if err != nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: level,
		})))
		slog.Warn("file logging unavailable: failed to open log file", "path", logPath, "error", err)
		return func() {}
	}

	mw := io.MultiWriter(os.Stderr, rf)
	slog.SetDefault(slog.New(slog.NewTextHandler(mw, &slog.HandlerOptions{
		Level: level,
	})))

	// SIGHUP reopens the log file so logrotate can rotate in place.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	go func() {
		for range sigCh {
			if err := rf.Reopen(); err != nil {
				slog.Error("failed to reopen log file on SIGHUP", "path", logPath, "error", err)
			} else {
				slog.Info("log file reopened", "path", logPath)
			}
		}
	}()

	return func() {
		signal.Stop(sigCh)
		close(sigCh)
		rf.Close()
	}
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// reopenableFile is an io.Writer backed by an *os.File that can be atomically
// reopened (for log rotation via SIGHUP). All operations are goroutine-safe.
type reopenableFile struct {
	path string
	mu   sync.Mutex
	f    *os.File
}

func newReopenableFile(path string) (*reopenableFile, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		return nil, err
	}
	return &reopenableFile{path: path, f: f}, nil
}

func (r *reopenableFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Write(p)
}

// Reopen closes the current file and opens a new one at the same path.
// This is the mechanism that makes logrotate's copytruncate-free rotation work:
// logrotate renames the old file, then sends SIGHUP, and the process opens a
// fresh file at the original path.
func (r *reopenableFile) Reopen() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Open the new file before closing the old one so a failed open doesn't
	// leave the writer in a broken state with a closed fd.
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	r.f.Close()
	r.f = f
	return nil
}

func (r *reopenableFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}
