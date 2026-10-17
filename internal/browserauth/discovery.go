package browserauth

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/magarcia/expensify-cli/internal/fault"
)

func resolveBrowser(browser string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(browser))
	if name == "" {
		name = "auto"
	}
	if name != "auto" && name != "chrome" && name != "chromium" && name != "edge" && name != "brave" {
		if path, err := exec.LookPath(browser); err == nil {
			return path, nil
		}
		return "", missingBrowser()
	}
	home, _ := os.UserHomeDir()
	candidates := browserCandidates(name, runtime.GOOS, home, os.Getenv)
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", missingBrowser()
}

func missingBrowser() error {
	return fault.New("AUTH-007", "No supported browser was found.", "Install Chrome, Chromium, Edge, or Brave; choose an executable with --browser; or use --manual.", 3)
}

func browserCandidates(name, system, home string, getenv func(string) string) []string {
	names := []string{name}
	if name == "auto" {
		names = []string{"chrome", "chromium", "edge", "brave"}
	}
	var candidates []string
	for _, name := range names {
		switch system {
		case "darwin":
			application := map[string]string{
				"chrome":   "Google Chrome.app/Contents/MacOS/Google Chrome",
				"chromium": "Chromium.app/Contents/MacOS/Chromium",
				"edge":     "Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
				"brave":    "Brave Browser.app/Contents/MacOS/Brave Browser",
			}[name]
			candidates = append(candidates, filepath.Join("/Applications", application), filepath.Join(home, "Applications", application))
		case "windows":
			application := map[string]string{
				"chrome":   "Google/Chrome/Application/chrome.exe",
				"chromium": "Chromium/Application/chrome.exe",
				"edge":     "Microsoft/Edge/Application/msedge.exe",
				"brave":    "BraveSoftware/Brave-Browser/Application/brave.exe",
			}[name]
			for _, variable := range []string{"LOCALAPPDATA", "PROGRAMFILES", "PROGRAMFILES(X86)"} {
				if base := getenv(variable); base != "" {
					candidates = append(candidates, filepath.Join(base, filepath.FromSlash(application)))
				}
			}
		default:
			commands := map[string][]string{
				"chrome":   {"google-chrome", "google-chrome-stable", "chrome"},
				"chromium": {"chromium", "chromium-browser"},
				"edge":     {"microsoft-edge", "microsoft-edge-stable"},
				"brave":    {"brave-browser", "brave"},
			}[name]
			candidates = append(candidates, commands...)
		}
	}
	return candidates
}
