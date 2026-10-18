// Package cli provides the command-line interface without process-global state.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/magarcia/expensify-cli/internal/api"
	"github.com/magarcia/expensify-cli/internal/browserauth"
	"github.com/magarcia/expensify-cli/internal/config"
	"github.com/magarcia/expensify-cli/internal/expensify"
	"github.com/magarcia/expensify-cli/internal/fault"
	"github.com/magarcia/expensify-cli/internal/output"
	"github.com/spf13/pflag"
)

type options struct {
	help, version, json, noColor, quiet, noInput, yes, dryRun bool
	verbose                                                   int
	format, configDir, timeout                                string
}

type application struct {
	in           io.Reader
	out, errOut  io.Writer
	opts         options
	flags        *pflag.FlagSet
	values       map[string]*string
	bools        map[string]*bool
	store        *config.Store
	settings     config.Settings
	reader       *bufio.Reader
	browserLogin func(context.Context, browserauth.Options) (config.Credentials, error)
}

// Run executes one invocation. It never prints credentials or calls os.Exit.
func Run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer, version, commit, date string) int {
	a := &application{in: in, out: out, errOut: errOut, values: map[string]*string{}, bools: map[string]*bool{}}
	a.opts.format = earlyFormat(args)
	if len(args) > 0 && args[0] == "__complete" {
		return a.writeCompletionCandidates(args[1:])
	}
	commands := allCommands()
	words := commandWords(args, commands)
	if len(words) > 0 && words[0] == "help" {
		words = words[1:]
		a.opts.help = true
		if len(words) == 1 {
			if topic := helpTopic(words[0]); topic != "" {
				_, err := fmt.Fprintln(out, topic)
				return a.finish(err)
			}
		}
		args = append(append([]string(nil), words...), "--help")
	}
	selected := findCommand(commands, words)
	a.flags = a.flagSet(selected)
	if err := a.flags.Parse(args); err != nil {
		return a.finish(fault.Usage(safeParseError(err)))
	}
	if a.opts.json {
		a.opts.format = "json"
	}
	if a.opts.version {
		_, err := fmt.Fprintf(out, "expensify %s (commit %s, built %s)\n", version, commit, date)
		return a.finish(err)
	}
	words = a.flags.Args()
	if len(words) > 0 && words[0] == "help" {
		words = words[1:]
	}
	if len(words) == 0 {
		return a.finish(a.rootHelp(commands, a.opts.help))
	}
	if words[0] == "completion" {
		if a.opts.help || len(words) == 1 {
			return a.finish(a.completionHelp())
		}
		if len(words) != 2 {
			return a.finish(fault.Usage("Choose one completion shell: bash, zsh, fish, or powershell."))
		}
		return a.finish(a.completion(words[1]))
	}
	if len(words) == 1 {
		if hasNoun(commands, words[0]) {
			return a.finish(a.nounHelp(commands, words[0]))
		}
		return a.finish(unknownCommand(words[0], nouns(commands)))
	}
	selected = findCommand(commands, words)
	if selected == nil {
		return a.finish(unknownCommand(strings.Join(words[:2], " "), commandNames(commands)))
	}
	if len(words) > 2 {
		return a.finish(fault.Usage("Unexpected positional arguments. Use named flags for command inputs."))
	}
	if a.opts.help {
		return a.finish(a.commandHelp(*selected))
	}
	a.store = config.New(a.opts.configDir)
	settings, err := config.LoadSettings(a.store)
	if err != nil {
		return a.finish(err)
	}
	a.settings = settings
	if !a.flags.Changed("output") && !a.opts.json {
		a.opts.format = settings.Output
	}
	if a.flags.Changed("timeout") {
		a.settings.Timeout = a.opts.timeout
	}
	if err := config.ValidateSettings(config.Settings{Output: a.opts.format, Timeout: a.settings.Timeout}); err != nil {
		return a.finish(err)
	}
	if words[0] == "auth" || words[0] == "config" {
		if err := a.validateFlags(*selected); err != nil {
			return a.finish(err)
		}
		if words[0] == "auth" {
			return a.finish(a.auth(ctx, words[1]))
		}
		return a.finish(a.configure(words[1]))
	}
	values := a.flagValues()
	if selected.Name == "expense create" && !a.flags.Changed("currency") {
		values["currency"] = settings.Currency
	}
	inferReportPolicy := selected.Name == "expense create" && values["report-id"] != ""
	if _, ok := a.values["policy-id"]; ok && !a.flags.Changed("policy-id") && !inferReportPolicy {
		values["policy-id"] = settings.PolicyID
	}
	for _, flag := range selected.Flags {
		if flag.Required && strings.TrimSpace(values[flag.Name]) == "" {
			return a.finish(fault.Usage("Missing required flag: --" + flag.Name + "."))
		}
	}
	if err := expensify.Validate(words[0], words[1], values); err != nil {
		return a.finish(err)
	}
	if a.opts.dryRun {
		return a.finish(a.write(map[string]any{"dryRun": true, "command": selected.Name, "parameters": values, "mutates": selected.Mutates}))
	}
	if selected.Confirm {
		if err := a.confirm("Continue with expensify " + selected.Name + "?"); err != nil {
			return a.finish(err)
		}
	}
	client, _, err := a.client(ctx, true)
	if err != nil {
		return a.finish(err)
	}
	stop := a.progress("Running " + selected.Name)
	value, err := expensify.New(client).Execute(ctx, words[0], words[1], values)
	stop()
	if err != nil {
		return a.finish(err)
	}
	if selected.Mutates && !a.opts.quiet && a.opts.format != "json" && terminal(a.errOut) {
		fmt.Fprintf(a.errOut, "Completed. Next: expensify %s list\n", words[0])
	}
	return a.finish(a.write(value))
}

func (a *application) flagSet(selected *expensify.Spec) *pflag.FlagSet {
	f := pflag.NewFlagSet("expensify", pflag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.BoolVarP(&a.opts.help, "help", "h", a.opts.help, "Show help")
	f.BoolVar(&a.opts.version, "version", false, "Show version, commit, and build date")
	f.CountVarP(&a.opts.verbose, "verbose", "v", "Show additional diagnostics without credentials")
	f.StringVarP(&a.opts.format, "output", "o", a.opts.format, "Output format: auto, text, json, llm")
	f.BoolVar(&a.opts.json, "json", false, "Shorthand for --output json")
	f.BoolVar(&a.opts.noColor, "no-color", false, "Disable color and animated progress")
	f.BoolVarP(&a.opts.quiet, "quiet", "q", false, "Suppress progress and other nonessential messages")
	f.BoolVar(&a.opts.noInput, "no-input", false, "Never prompt for input")
	f.BoolVarP(&a.opts.yes, "yes", "y", false, "Skip action confirmations")
	f.BoolVar(&a.opts.dryRun, "dry-run", false, "Validate and preview without making changes or API calls")
	f.StringVar(&a.opts.configDir, "config-dir", "", "Private directory for settings and credentials")
	f.StringVar(&a.opts.timeout, "timeout", "30s", "Request timeout, such as 30s or 2m")
	if selected != nil {
		for _, flag := range selected.Flags {
			if flag.Bool {
				a.bools[flag.Name] = f.Bool(flag.Name, flag.Default == "true", flag.Usage)
			} else {
				a.values[flag.Name] = f.String(flag.Name, flag.Default, flag.Usage)
			}
		}
	}
	return f
}

func (a *application) validateFlags(cmd expensify.Spec) error {
	var missing []string
	for _, flag := range cmd.Flags {
		if !flag.Required {
			continue
		}
		if value := a.values[flag.Name]; value != nil && strings.TrimSpace(*value) == "" {
			missing = append(missing, "--"+flag.Name)
		}
	}
	if len(missing) > 0 {
		return fault.Usage("Missing required flags: " + strings.Join(missing, ", ") + ".")
	}
	return nil
}

func (a *application) flagValues() map[string]string {
	values := make(map[string]string, len(a.values)+len(a.bools))
	for name, value := range a.values {
		if *value != "" || a.flags.Changed(name) {
			values[name] = *value
		}
	}
	for name, value := range a.bools {
		if a.flags.Changed(name) {
			if *value {
				values[name] = "true"
			} else {
				values[name] = "false"
			}
		}
	}
	return values
}

func (a *application) client(ctx context.Context, required bool) (*api.Client, string, error) {
	credentials, source, err := config.Resolve(config.Credentials{})
	if err != nil {
		return nil, "", err
	}
	if source == "none" {
		stored, err := a.store.Load()
		if err != nil {
			return nil, "", err
		}
		credentials, source, err = config.Resolve(stored)
		if err != nil {
			return nil, "", err
		}
	}
	if required && credentials.AuthToken == "" && credentials.PartnerUserID == "" {
		return nil, source, fault.New("AUTH-001", "No Expensify credentials are available.", "Run expensify auth login, or set the EXPENSIFY authentication environment variables.", 3)
	}
	client, err := a.newClient(credentials, source == "stored")
	if err != nil {
		return nil, source, err
	}
	return client, source, nil
}

func (a *application) newClient(credentials config.Credentials, save bool) (*api.Client, error) {
	duration, _ := time.ParseDuration(a.settings.Timeout)
	opts := api.Options{BaseURL: os.Getenv("EXPENSIFY_API_URL"), HTTPClient: &http.Client{Timeout: duration}, Credentials: credentials}
	if save {
		opts.SaveCredentials = a.store.Save
	}
	return api.New(opts)
}

func (a *application) write(value any) error {
	return output.Write(a.out, value, a.opts.format, terminal(a.out), a.opts.noColor || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb")
}

func (a *application) finish(err error) int {
	if err == nil || errors.Is(err, syscall.EPIPE) {
		return 0
	}
	if errors.Is(err, context.Canceled) {
		return 130
	}
	var failure *fault.Error
	if !errors.As(err, &failure) {
		failure = fault.New("CLI-003", "The command could not complete.", "Check output permissions and try again.", 1)
	}
	if a.opts.format == "json" || a.opts.json {
		_ = json.NewEncoder(a.errOut).Encode(map[string]any{"error": failure})
	} else {
		fmt.Fprintf(a.errOut, "Error [%s]: %s\n", failure.Code, failure.Message)
		if failure.Hint != "" {
			fmt.Fprintln(a.errOut, failure.Hint)
		}
	}
	return failure.Exit
}

func terminal(value any) bool {
	file, ok := value.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(file.Fd())
}

func (a *application) confirm(message string) error {
	if a.opts.yes {
		return nil
	}
	if a.opts.noInput || a.opts.format == "json" || !terminal(a.in) {
		return fault.New("CLI-002", "This action requires confirmation.", "Review the command with --dry-run, then pass --yes to continue.", 2)
	}
	fmt.Fprint(a.errOut, message+" [y/N] ")
	line, err := a.readLine()
	if err != nil && !errors.Is(err, io.EOF) {
		return fault.Usage("Could not read confirmation. Pass --yes to confirm the action.")
	}
	if strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes") {
		return nil
	}
	return fault.New("CLI-002", "The action was canceled.", "Run the command again when ready.", 2)
}

func (a *application) readLine() (string, error) {
	if a.reader == nil {
		a.reader = bufio.NewReader(a.in)
	}
	return a.reader.ReadString('\n')
}

func (a *application) progress(message string) func() {
	if a.opts.quiet || a.opts.format == "json" || !terminal(a.errOut) && a.opts.verbose == 0 {
		return func() {}
	}
	fmt.Fprintln(a.errOut, message+"…")
	started := time.Now()
	return func() {
		if a.opts.verbose > 0 {
			fmt.Fprintf(a.errOut, "Request completed in %s.\n", time.Since(started).Round(time.Millisecond))
		}
	}
}

func allCommands() []expensify.Spec {
	commands := []expensify.Spec{
		{Name: "auth login", Summary: "Sign in to New Expensify in a browser", Example: "expensify auth login", Mutates: true, Flags: []expensify.Flag{
			{Name: "browser", Usage: "Browser: auto, chrome, chromium, edge, brave, or an executable path", Default: "auto"},
			{Name: "login-timeout", Usage: "Maximum time to finish browser sign-in", Default: "5m"},
			{Name: "manual", Usage: "Paste device credentials at hidden prompts", Bool: true},
			{Name: "credentials-file", Usage: "Read credential JSON from a file"},
			{Name: "credentials-stdin", Usage: "Read credential JSON from standard input", Bool: true},
		}},
		{Name: "auth status", Summary: "Inspect authentication without revealing credentials", Example: "expensify auth status --check --output json", Flags: []expensify.Flag{{Name: "check", Usage: "Verify the session with Expensify", Bool: true}}},
		{Name: "auth logout", Summary: "Remove locally saved credentials", Example: "expensify auth logout", Mutates: true},
		{Name: "config list", Summary: "Show effective nonsecret settings", Example: "expensify config list --output json"},
		{Name: "config get", Summary: "Read one effective setting", Example: "expensify config get --key currency", Flags: []expensify.Flag{{Name: "key", Usage: "Setting: output, policy-id, currency, timeout", Required: true}}},
		{Name: "config set", Summary: "Save one user setting", Example: "expensify config set --key currency --value EUR", Mutates: true, Flags: []expensify.Flag{{Name: "key", Usage: "Setting: output, policy-id, currency, timeout", Required: true}, {Name: "value", Usage: "New setting value"}}},
	}
	return append(commands, expensify.Specs()...)
}

func findCommand(commands []expensify.Spec, words []string) *expensify.Spec {
	if len(words) < 2 {
		return nil
	}
	name := words[0] + " " + words[1]
	for i := range commands {
		if commands[i].Name == name {
			return &commands[i]
		}
	}
	return nil
}

func commandWords(args []string, commands []expensify.Spec) []string {
	valueFlags := map[string]bool{"output": true, "o": true, "config-dir": true, "timeout": true}
	for _, cmd := range commands {
		for _, flag := range cmd.Flags {
			if !flag.Bool {
				valueFlags[flag.Name] = true
			}
		}
	}
	var words []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			words = append(words, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			name := strings.TrimLeft(arg, "-")
			if strings.Contains(name, "=") {
				continue
			}
			if valueFlags[name] && i+1 < len(args) {
				i++
			}
			continue
		}
		words = append(words, arg)
	}
	return words
}

func earlyFormat(args []string) string {
	format := os.Getenv("EXPENSIFY_OUTPUT")
	if format == "" {
		format = "auto"
	}
	for i, arg := range args {
		if arg == "--json" {
			return "json"
		}
		if strings.HasPrefix(arg, "--output=") {
			format = strings.TrimPrefix(arg, "--output=")
		}
		if (arg == "--output" || arg == "-o") && i+1 < len(args) {
			format = args[i+1]
		}
	}
	return format
}

func safeParseError(err error) string {
	message := err.Error()
	if strings.HasPrefix(message, "invalid argument ") {
		return "A flag has an invalid value. Use --help to check accepted values."
	}
	if strings.HasPrefix(message, "unknown shorthand flag:") {
		return "Unknown short flag. Use --help to see supported flags."
	}
	return message
}

func commandNames(commands []expensify.Spec) []string {
	names := make([]string, 0, len(commands))
	for _, cmd := range commands {
		names = append(names, cmd.Name)
	}
	return names
}
func nouns(commands []expensify.Spec) []string {
	seen := map[string]bool{"completion": true, "help": true}
	for _, cmd := range commands {
		seen[strings.Fields(cmd.Name)[0]] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func hasNoun(commands []expensify.Spec, noun string) bool {
	for _, name := range nouns(commands) {
		if name == noun {
			return true
		}
	}
	return false
}

func unknownCommand(name string, candidates []string) error {
	hint := "Run expensify --help to see available commands."
	best, distance := "", 4
	for _, candidate := range candidates {
		if d := editDistance(name, candidate); d < distance {
			best, distance = candidate, d
		}
	}
	if best != "" {
		hint = fmt.Sprintf("Did you mean %q? Run expensify --help for available commands.", best)
	}
	return fault.New("CLI-001", fmt.Sprintf("Unknown command %q.", name), hint, 2)
}

func editDistance(a, b string) int {
	x, y := []rune(a), []rune(b)
	previous := make([]int, len(y)+1)
	for j := range previous {
		previous[j] = j
	}
	for i, c := range x {
		current := make([]int, len(y)+1)
		current[0] = i + 1
		for j, d := range y {
			cost := 0
			if c != d {
				cost = 1
			}
			current[j+1] = min(current[j]+1, previous[j+1]+1, previous[j]+cost)
		}
		previous = current
	}
	return previous[len(y)]
}
