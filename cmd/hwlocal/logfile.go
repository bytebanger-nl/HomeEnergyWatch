package main

import (
	"os"
	"runtime/debug"
	"sync"
)

// logFile is an append-only log file that is moved to <name>.1 once it
// exceeds max bytes, so a long-running service keeps at most two files.
type logFile struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
	// crash: the Go runtime also writes fatal errors (an unrecovered panic
	// and its stack traces) to this file, see captureCrashes.
	crash bool
}

func openLogFile(path string, max int64) (*logFile, error) {
	l := &logFile{path: path, max: max}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *logFile) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, fi.Size()
	if l.crash {
		_ = debug.SetCrashOutput(f, debug.CrashOptions{})
	}
	return nil
}

// captureCrashes makes the Go runtime write a fatal error, such as a panic
// nobody recovers, with its stack traces to the log file as well as to
// stderr. A Windows service has no stderr, so without this the cause of a
// crash would be lost.
func (l *logFile) captureCrashes() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return os.ErrClosed
	}
	l.crash = true
	return debug.SetCrashOutput(l.f, debug.CrashOptions{})
}

func (l *logFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return 0, os.ErrClosed
	}
	if l.size > 0 && l.size+int64(len(p)) > l.max {
		if l.crash {
			// the runtime holds its own handle, which would keep Windows
			// from renaming the file
			_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
		}
		l.f.Close()
		l.f = nil
		_ = os.Rename(l.path, l.path+".1") // replaces the previous .1
		if err := l.open(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

func (l *logFile) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
