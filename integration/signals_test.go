//go:build !windows

package integration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSignalsInterruptBlockedInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		signal os.Signal
		code   int
	}{
		{name: "interrupt", signal: os.Interrupt, code: 130},
		{name: "terminate", signal: syscall.SIGTERM, code: 143},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandbox(t, "http://127.0.0.1:1", nil)
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			path := filepath.Join(s.dir, "credentials.fifo")
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := s.command(ctx, "", "auth", "login", "--credentials-file", path, "--output", "json")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			// Opening the writer confirms the CLI reached authentication after
			// registering signal handlers. Keep JSON incomplete to block its read.
			input := openCredentialFIFO(t, ctx, path)
			defer input.Close()
			if _, err := input.Write([]byte("{")); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Process.Signal(tc.signal); err != nil {
				t.Fatal(err)
			}
			err := cmd.Wait()
			failure, ok := err.(*exec.ExitError)
			if ctx.Err() != nil || !ok || failure.ExitCode() != tc.code || stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("signal %v: err=%v stdout=%q stderr=%q", tc.signal, err, stdout.String(), stderr.String())
			}
		})
	}
}

func openCredentialFIFO(t *testing.T, ctx context.Context, path string) *os.File {
	t.Helper()
	for {
		input, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			return input
		}
		if !errors.Is(err, syscall.ENXIO) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("CLI did not open the credential file")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestSignalCleansUpOwnedBrowser(t *testing.T) {
	for _, tc := range []struct {
		name   string
		signal os.Signal
		code   int
	}{{"interrupt", os.Interrupt, 130}, {"terminate", syscall.SIGTERM, 143}} {
		t.Run(tc.name, func(t *testing.T) {
			browser := startWaitingBrowser(t, false)
			if err := browser.command.Process.Signal(tc.signal); err != nil {
				t.Fatal(err)
			}
			browser.wait(t, tc.code)
			if _, err := os.Stat(browser.profile); !os.IsNotExist(err) {
				t.Fatalf("temporary browser profile remains after signal: %v", err)
			}
			if err := syscall.Kill(browser.pid, 0); err != syscall.ESRCH {
				t.Fatalf("owned browser remains after signal: %v", err)
			}
		})
	}
}

func TestSecondSignalExitsImmediately(t *testing.T) {
	for _, tc := range []struct {
		name   string
		signal os.Signal
		code   int
	}{{"interrupt", os.Interrupt, 130}, {"terminate", syscall.SIGTERM, 143}} {
		t.Run(tc.name, func(t *testing.T) {
			browser := startWaitingBrowser(t, true)
			if err := browser.command.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
			time.Sleep(100 * time.Millisecond)
			sentAt := time.Now()
			if err := browser.command.Process.Signal(tc.signal); err != nil {
				t.Fatal(err)
			}
			browser.wait(t, tc.code)
			if time.Since(sentAt) >= time.Second {
				t.Fatal("second signal waited for browser cleanup")
			}
		})
	}
}

type waitingBrowser struct {
	command        *exec.Cmd
	stdout, stderr bytes.Buffer
	pid            int
	profile        string
}

func startWaitingBrowser(t *testing.T, ignoreTerm bool) *waitingBrowser {
	t.Helper()
	s := newSandbox(t, "http://127.0.0.1:1", nil)
	path := filepath.Join(s.dir, "fake-chromium")
	script := `#!/bin/sh
for argument in "$@"; do
    case "$argument" in
        --user-data-dir=*) profile=${argument#--user-data-dir=} ;;
    esac
done
if [ "$EXPENSIFY_TEST_IGNORE_TERM" = "1" ]; then
    trap '' TERM INT
else
    trap 'exit 0' TERM INT
fi
printf '%s\n%s\n' "$$" "$profile" > "$EXPENSIFY_TEST_BROWSER_MARKER"
while :; do sleep 1; done
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	marker := filepath.Join(s.dir, "browser.ready")
	command := s.command(ctx, "", "auth", "login", "--browser", path, "--output", "json", "--quiet")
	command.Env = append(command.Env, "EXPENSIFY_TEST_BROWSER_MARKER="+marker)
	if ignoreTerm {
		command.Env = append(command.Env, "EXPENSIFY_TEST_IGNORE_TERM=1")
	}
	browser := &waitingBrowser{command: command}
	command.Stdout, command.Stderr = &browser.stdout, &browser.stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if browser.pid != 0 {
			_ = syscall.Kill(-browser.pid, syscall.SIGKILL)
		}
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	for {
		data, err := os.ReadFile(marker)
		if err == nil {
			parts := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(parts) == 2 {
				browser.pid, err = strconv.Atoi(parts[0])
				if err != nil {
					t.Fatal(err)
				}
				browser.profile = parts[1]
				return browser
			}
		}
		select {
		case <-ctx.Done():
			_ = command.Wait()
			t.Fatalf("browser did not start: %s", browser.stderr.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (browser *waitingBrowser) wait(t *testing.T, code int) {
	t.Helper()
	err := browser.command.Wait()
	failure, ok := err.(*exec.ExitError)
	if !ok || failure.ExitCode() != code || browser.stdout.Len() != 0 || browser.stderr.Len() != 0 {
		t.Fatalf("signal: err=%v stdout=%q stderr=%q", err, browser.stdout.String(), browser.stderr.String())
	}
}

func TestClosedOutputPipeExitsSilently(t *testing.T) {
	s := newSandbox(t, "http://127.0.0.1:1", nil)
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	readEnd.Close()
	defer writeEnd.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := s.command(ctx, "", "completion", "bash")
	cmd.Stdout = writeEnd
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil || stderr.Len() != 0 {
		t.Fatalf("closed pipe: err=%v stderr=%q", err, stderr.String())
	}
}
