// Package browserauth signs in through an isolated, short-lived Chromium profile.
package browserauth

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/magarcia/expensify-cli/internal/config"
	"github.com/magarcia/expensify-cli/internal/fault"
)

const defaultTimeout = 5 * time.Minute

type Options struct {
	Browser       string
	Timeout       time.Duration
	ProfileParent string
}

// Login opens a browser owned by this invocation and removes its private profile
// after the browser stops. It never attaches to an existing browser session.
func Login(ctx context.Context, options Options) (config.Credentials, error) {
	return login(ctx, options, dependencies{
		resolve: resolveBrowser,
		profile: config.PrivateTempDir,
		start:   startBrowser,
		dial:    dialCDP,
		remove:  os.RemoveAll,
	})
}

type browserProcess interface {
	Done() <-chan struct{}
	Stop()
}

type dependencies struct {
	resolve func(string) (string, error)
	profile func(string, string) (string, error)
	start   func(string, string) (browserProcess, error)
	dial    func(context.Context, string) (protocol, error)
	remove  func(string) error
}

func login(parent context.Context, options Options, deps dependencies) (credentials config.Credentials, resultErr error) {
	if options.Timeout < 0 {
		return credentials, fault.Usage("Browser login timeout must be positive.")
	}
	if options.Timeout == 0 {
		options.Timeout = defaultTimeout
	}
	if err := parent.Err(); err != nil {
		return credentials, err
	}
	path, err := deps.resolve(options.Browser)
	if err != nil {
		return credentials, err
	}
	profile, err := deps.profile(options.ProfileParent, ".browser-login-")
	if err != nil {
		return credentials, err
	}
	defer func() {
		if err := deps.remove(profile); err != nil {
			credentials = config.Credentials{}
			resultErr = fault.New("AUTH-008", "Cannot remove the temporary browser profile.", "Remove .browser-login-* directories from your Expensify configuration directory before retrying.", 3)
		}
	}()
	process, err := deps.start(path, profile)
	if err != nil {
		return credentials, browserError("Cannot start the browser for sign-in.")
	}
	defer process.Stop()

	ctx, cancel := context.WithTimeout(parent, options.Timeout)
	defer cancel()
	browserCtx, browserCancel := context.WithCancel(ctx)
	defer browserCancel()
	go func() {
		select {
		case <-process.Done():
			browserCancel()
		case <-browserCtx.Done():
		}
	}()
	classify := func() error {
		if parent.Err() != nil {
			return parent.Err()
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fault.New("AUTH-009", "Browser sign-in timed out.", "Run expensify auth login again, or increase --login-timeout to allow more time to sign in.", 3)
		}
		return browserError("The sign-in browser closed or its connection failed.")
	}

	endpoint, err := waitEndpoint(browserCtx, profile, process.Done())
	if err != nil {
		return credentials, classify()
	}
	connection, err := deps.dial(browserCtx, endpoint)
	if err != nil {
		return credentials, classify()
	}
	defer connection.Close()
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer closeCancel()
		_ = connection.Call(closeCtx, "Browser.close", nil, "", nil)
	}()
	if err := connection.Call(browserCtx, "Target.createTarget", map[string]any{"url": "https://new.expensify.com"}, "", nil); err != nil {
		return credentials, classify()
	}
	credentials, err = pollCredentials(browserCtx, connection)
	if err != nil {
		return config.Credentials{}, classify()
	}
	return credentials, nil
}

func browserError(message string) error {
	return fault.New("AUTH-008", message, "Retry expensify auth login, or use --manual to enter device credentials.", 3)
}

type ownedProcess struct {
	command *exec.Cmd
	done    chan struct{}
	control *processControl
}

func startBrowser(path, profile string) (browserProcess, error) {
	command := exec.Command(path, browserArguments(profile)...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	control, err := configureProcess(command)
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		control.close()
		return nil, err
	}
	if err := control.started(command); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		control.close()
		return nil, err
	}
	process := &ownedProcess{command: command, done: make(chan struct{}), control: control}
	go func() {
		_ = command.Wait()
		close(process.done)
	}()
	return process, nil
}

func browserArguments(profile string) []string {
	return []string{
		"--user-data-dir=" + profile,
		"--remote-debugging-port=0",
		"--remote-debugging-address=127.0.0.1",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-sync",
		"about:blank",
	}
}

func (p *ownedProcess) Done() <-chan struct{} { return p.done }

func (p *ownedProcess) Stop() {
	p.control.stop(p.command, p.done)
}

func waitEndpoint(ctx context.Context, profile string, processDone <-chan struct{}) (string, error) {
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		endpoint, err := readEndpoint(filepath.Join(profile, "DevToolsActivePort"))
		if err == nil {
			return endpoint, nil
		}
		select {
		case <-startup.Done():
			return "", startup.Err()
		case <-processDone:
			return "", errors.New("browser exited")
		case <-ticker.C:
		}
	}
}

func readEndpoint(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return "", errors.New("invalid debugging endpoint file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return "", errors.New("debugging endpoint file changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return "", errors.New("invalid debugging endpoint file")
	}
	return parseEndpoint(string(data))
}

func parseEndpoint(data string) (string, error) {
	lines := strings.Split(strings.TrimSpace(data), "\n")
	if len(lines) != 2 {
		return "", errors.New("invalid debugging endpoint")
	}
	portText := strings.TrimSpace(lines[0])
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != portText {
		return "", errors.New("invalid debugging port")
	}
	path := strings.TrimSpace(lines[1])
	const prefix = "/devtools/browser/"
	if !strings.HasPrefix(path, prefix) || len(path) <= len(prefix) || len(path) > 256 {
		return "", errors.New("invalid debugging path")
	}
	for _, char := range strings.TrimPrefix(path, prefix) {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-') {
			return "", errors.New("invalid debugging path")
		}
	}
	return "ws://127.0.0.1:" + portText + path, nil
}
