package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/magarcia/expensify-cli/internal/cli"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	restoreTerminal := terminalRestorer(os.Stdin.Fd())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGPIPE)
	defer signal.Stop(signals)
	var interrupted atomic.Int32
	go func() {
		for sig := range signals {
			restoreTerminal()
			if sig == syscall.SIGPIPE {
				os.Exit(0)
			}
			code := int32(130)
			if sig == syscall.SIGTERM {
				code = 143
			}
			if interrupted.Swap(code) != 0 {
				os.Exit(int(code))
			}
			cancel()
			// Allow browser cleanup to finish while bounding context-unaware reads.
			time.AfterFunc(5*time.Second, func() { os.Exit(int(code)) })
		}
	}()
	exit := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, version, commit, date)
	if code := interrupted.Load(); code != 0 {
		exit = int(code)
	}
	restoreTerminal()
	os.Exit(exit)
}

func terminalRestorer(fd uintptr) func() {
	if !term.IsTerminal(fd) {
		return func() {}
	}
	state, err := term.GetState(fd)
	if err != nil {
		return func() {}
	}
	var once sync.Once
	return func() { once.Do(func() { _ = term.Restore(fd, state) }) }
}
