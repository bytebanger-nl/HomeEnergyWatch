package main

import (
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestLogFileRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hwlocal.log")
	l, err := openLogFile(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"first line\n", "second line\n", "third line\n"} {
		if _, err := l.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	cur, _ := os.ReadFile(path)
	old, _ := os.ReadFile(path + ".1")
	if string(cur) != "third line\n" || string(old) != "second line\n" {
		t.Fatalf("current %q, previous %q", cur, old)
	}

	// reopening appends and counts the existing size
	l, err = openLogFile(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := l.Write([]byte("fourth line\n")); err != nil {
		t.Fatal(err)
	}
	old, _ = os.ReadFile(path + ".1")
	if !strings.HasPrefix(string(old), "third") {
		t.Fatalf("previous %q, want the third line", old)
	}
}

// With crash capture on, the runtime holds its own handle to the log file;
// rotating must still work (Windows refuses to rename a file that is open).
func TestLogFileRotatesWithCrashOutput(t *testing.T) {
	defer debug.SetCrashOutput(nil, debug.CrashOptions{})
	path := filepath.Join(t.TempDir(), "hwlocal.log")
	l, err := openLogFile(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.captureCrashes(); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"first line\n", "second line\n"} {
		if _, err := l.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	old, _ := os.ReadFile(path + ".1")
	if string(old) != "first line\n" {
		t.Fatalf("previous %q, want the first line", old)
	}
	// release the runtime's handle before TempDir's cleanup
	_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

// A panic nobody recovers ends up in the log file with its stack trace. The
// test runs itself again in a child process that crashes.
func TestLogFileCapturesCrash(t *testing.T) {
	if path := os.Getenv("HWLOCAL_TEST_CRASH_LOG"); path != "" {
		l, err := openLogFile(path, 1<<20)
		if err != nil {
			os.Exit(3)
		}
		_ = l.captureCrashes()
		panic("boom for the log")
	}
	path := filepath.Join(t.TempDir(), "hwlocal.log")
	cmd := exec.Command(os.Args[0], "-test.run=^TestLogFileCapturesCrash$")
	cmd.Env = append(os.Environ(), "HWLOCAL_TEST_CRASH_LOG="+path)
	if err := cmd.Run(); err == nil {
		t.Fatal("child did not crash")
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "panic: boom for the log") || !strings.Contains(string(got), "goroutine ") {
		t.Fatalf("log file lacks the panic and stack:\n%s", got)
	}
}

func TestParseConfigFlags(t *testing.T) {
	fs := newTestFlagSet()
	c, err := parseConfig(fs, []string{"-data", `D:\hw`, "-tls", "selfsigned"}, "default")
	if err != nil {
		t.Fatal(err)
	}
	if c.dataDir != `D:\hw` || c.tlsMode != "selfsigned" {
		t.Fatalf("got data %q tls %q", c.dataDir, c.tlsMode)
	}
	if _, err := parseConfig(newTestFlagSet(), []string{"-nope"}, "d"); err == nil {
		t.Fatal("unknown flag accepted")
	}
	if _, err := parseConfig(newTestFlagSet(), []string{"extra"}, "d"); err == nil {
		t.Fatal("stray argument accepted")
	}
}

func newTestFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}
