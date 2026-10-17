package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/magarcia/expensify-cli/internal/api"
	"github.com/magarcia/expensify-cli/internal/browserauth"
	"github.com/magarcia/expensify-cli/internal/config"
	"github.com/magarcia/expensify-cli/internal/fault"
)

func (a *application) auth(ctx context.Context, verb string) error {
	switch verb {
	case "login":
		return a.login(ctx)
	case "status":
		return a.authStatus(ctx)
	case "logout":
		selection, err := a.store.CredentialStore()
		if err != nil {
			return err
		}
		if a.opts.dryRun {
			return a.write(a.authPreview("auth logout", selection))
		}
		if err := a.store.Logout(); err != nil {
			return err
		}
		clearedStores := []string{"file"}
		if selection != "file" {
			clearedStores = []string{"keyring", "file"}
		}
		return a.write(map[string]any{"loggedOut": true, "storage": "none", "path": "", "clearedStores": clearedStores, "environmentCredentialsUnaffected": true})
	}
	return fault.Usage("Unknown authentication command.")
}

func (a *application) login(ctx context.Context) error {
	selection, err := a.store.CredentialStore()
	if err != nil {
		return err
	}
	file := *a.values["credentials-file"]
	stdin := *a.bools["credentials-stdin"]
	manual := *a.bools["manual"]
	explicitBrowser := a.flags.Changed("browser")
	choices := 0
	for _, selected := range []bool{file != "", stdin, manual, explicitBrowser} {
		if selected {
			choices++
		}
	}
	if choices > 1 {
		return fault.Usage("Choose one login method: --browser, --manual, --credentials-file, or --credentials-stdin.")
	}
	loginTimeout, err := time.ParseDuration(*a.values["login-timeout"])
	if err != nil || loginTimeout <= 0 {
		return fault.Usage("--login-timeout must be a positive duration, such as 5m.")
	}
	if explicitBrowser && strings.TrimSpace(*a.values["browser"]) == "" {
		return fault.Usage("--browser must name a supported browser or an executable path.")
	}
	if a.opts.dryRun {
		mode := "browser"
		switch {
		case file != "":
			mode = "file"
		case stdin:
			mode = "stdin"
		case manual:
			mode = "manual"
		}
		preview := a.authPreview("auth login", selection)
		preview["mode"] = mode
		return a.write(preview)
	}
	var credentials config.Credentials
	browserSession := false
	switch {
	case file != "":
		var reader *os.File
		reader, err = os.Open(file)
		if err != nil {
			return fault.New("AUTH-006", "Cannot read the credential file.", "Check the file path and permissions. Use expensify help auth for the supported JSON format.", 3)
		}
		defer reader.Close()
		credentials, err = parseCredentials(reader)
	case stdin:
		if terminal(a.in) {
			return fault.Usage("--credentials-stdin expects piped JSON. Use --credentials-file or interactive login.")
		}
		credentials, err = parseCredentialsContext(ctx, a.in)
	case manual:
		credentials, err = a.promptCredentials()
	case explicitBrowser:
		browserSession = true
		credentials, err = a.loginInBrowser(ctx, loginTimeout)
	default:
		credentials, _, err = config.Resolve(config.Credentials{})
		if err == nil && credentials.AuthToken == "" && credentials.PartnerUserID == "" {
			if a.opts.noInput || a.opts.format == "json" || !terminal(a.in) {
				return fault.New("AUTH-001", "No credentials were provided.", "Run expensify auth login in a terminal, explicitly select --browser auto, or use --credentials-file / --credentials-stdin for headless login.", 3)
			}
			browserSession = true
			credentials, err = a.loginInBrowser(ctx, loginTimeout)
		}
	}
	if err != nil {
		return err
	}
	if err := validateCredentials(credentials); err != nil {
		return err
	}
	captured := credentials
	if browserSession {
		// Account identity must come from the verification response rather than
		// metadata inherited from the browser's asynchronously updated storage.
		credentials.AuthToken, credentials.Email, credentials.AccountID = "", "", 0
	}
	client, err := a.newClient(credentials, false)
	if err != nil {
		return err
	}
	stop := a.progress("Verifying New Expensify credentials")
	if credentials.PartnerUserID != "" {
		credentials, err = client.Authenticate(ctx)
	} else {
		_, err = client.Call(ctx, "ReconnectApp", url.Values{"policyIDList": {"[]"}})
	}
	stop()
	if err != nil {
		return err
	}
	if browserSession {
		accountID := credentials.AccountID
		if accountID == 0 {
			accountID, err = authenticatedAccountID(ctx, client)
			if err != nil {
				return err
			}
			credentials = client.Credentials()
		}
		if captured.AccountID <= 0 || accountID != captured.AccountID {
			return fault.New("AUTH-010", "The captured credentials don't match a verified signed-in account.", "Run expensify auth login again after browser sign-in has finished, or use --manual.", 3)
		}
		credentials.AccountID = accountID
		if credentials.Email == "" {
			credentials.Email = captured.Email
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.store.Save(credentials); err != nil {
		return err
	}
	if !a.opts.quiet && a.opts.format != "json" && terminal(a.errOut) {
		if a.store.Storage() == "keyring" {
			fmt.Fprintln(a.errOut, "Credentials saved in your OS keyring. Next: expensify workspace list")
		} else {
			fmt.Fprintf(a.errOut, "Credentials saved as plaintext in %s with private access permissions. Next: expensify workspace list\n", a.store.Path())
		}
	}
	storage, path := a.credentialStorage("stored")
	return a.write(map[string]any{"authenticated": true, "source": "stored", "storage": storage, "path": path, "refreshable": credentials.PartnerUserID != "", "email": credentials.Email})
}

func authenticatedAccountID(ctx context.Context, client *api.Client) (int64, error) {
	response, err := client.Call(ctx, "OpenApp", url.Values{"policyIDList": {"[]"}, "enablePriorityModeFilter": {"false"}})
	if err != nil {
		return 0, err
	}
	var accountID int64
	patches, _ := response["onyxData"].([]any)
	for _, value := range patches {
		patch, _ := value.(map[string]any)
		if patch["key"] != "session" {
			continue
		}
		switch patch["onyxMethod"] {
		case "set":
			accountID = 0
		case "merge":
		default:
			return 0, nil
		}
		session, ok := patch["value"].(map[string]any)
		if !ok {
			accountID = 0
			continue
		}
		if value, present := session["accountID"]; present {
			accountID = 0
			if number, ok := value.(json.Number); ok {
				accountID, _ = number.Int64()
			}
		}
	}
	return accountID, nil
}

func (a *application) loginInBrowser(ctx context.Context, timeout time.Duration) (config.Credentials, error) {
	login := a.browserLogin
	if login == nil {
		login = browserauth.Login
	}
	stop := a.progress("Opening a separate login window. Complete sign-in there; this command will continue automatically")
	defer stop()
	return login(ctx, browserauth.Options{Browser: *a.values["browser"], Timeout: timeout, ProfileParent: a.store.Dir()})
}

func (a *application) authStatus(ctx context.Context) error {
	if a.opts.dryRun {
		selection, err := a.store.CredentialStore()
		if err != nil {
			return err
		}
		command := "auth status"
		if *a.bools["check"] {
			command += " --check"
		}
		return a.write(a.authPreview(command, selection))
	}
	client, source, err := a.client(ctx, false)
	if err != nil {
		return err
	}
	credentials := client.Credentials()
	available := credentials.AuthToken != "" || credentials.PartnerUserID != ""
	checked := *a.bools["check"]
	if checked {
		if !available {
			return fault.New("AUTH-001", "No Expensify credentials are available.", "Run expensify auth login to authenticate.", 3)
		}
		stop := a.progress("Checking authentication")
		if credentials.AuthToken == "" {
			_, err = client.Authenticate(ctx)
		} else {
			_, err = client.Call(ctx, "ReconnectApp", url.Values{"policyIDList": {"[]"}})
		}
		stop()
		if err != nil {
			return err
		}
		credentials = client.Credentials()
	}
	storage, path := a.credentialStorage(source)
	return a.write(map[string]any{"authenticated": available, "checked": checked, "source": source, "storage": storage, "path": path, "hasSessionToken": credentials.AuthToken != "", "refreshable": credentials.PartnerUserID != "", "email": credentials.Email, "accountID": credentials.AccountID})
}

func (a *application) authPreview(command, selection string) map[string]any {
	return map[string]any{"dryRun": true, "command": command, "credentialStore": selection, "fallbackPath": a.store.Path()}
}

func (a *application) credentialStorage(source string) (storage, path string) {
	if source != "stored" {
		return source, ""
	}
	storage = a.store.Storage()
	if storage == "file" {
		path = a.store.Path()
	}
	return storage, path
}

func (a *application) promptCredentials() (config.Credentials, error) {
	if a.opts.noInput || a.opts.format == "json" || !terminal(a.in) {
		return config.Credentials{}, fault.New("AUTH-001", "No credentials were provided.", "Use --credentials-file, pipe JSON with --credentials-stdin, or set EXPENSIFY authentication environment variables. Run expensify help auth for setup.", 3)
	}
	fmt.Fprintln(a.errOut, "Sign in at https://new.expensify.com and copy the device credentials from browser storage.\nRun expensify help auth for extraction instructions. Both values are hidden while typing.")
	file := a.in.(interface{ Fd() uintptr })
	fmt.Fprint(a.errOut, "Device login: ")
	login, err := term.ReadPassword(file.Fd())
	fmt.Fprintln(a.errOut)
	if err != nil {
		return config.Credentials{}, fault.New("AUTH-006", "Could not read device credentials.", "Retry, or use --credentials-file.", 3)
	}
	fmt.Fprint(a.errOut, "Device secret: ")
	secret, err := term.ReadPassword(file.Fd())
	fmt.Fprintln(a.errOut)
	if err != nil {
		return config.Credentials{}, fault.New("AUTH-006", "Could not read device credentials.", "Retry, or use --credentials-file.", 3)
	}
	return config.Credentials{PartnerUserID: strings.TrimSpace(string(login)), PartnerUserSecret: strings.TrimSpace(string(secret))}, nil
}

func parseCredentials(reader io.Reader) (config.Credentials, error) {
	const maximum = 1 << 20
	data, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil || len(data) > maximum {
		return config.Credentials{}, credentialFormatError()
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&object) != nil || object == nil {
		return config.Credentials{}, credentialFormatError()
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return config.Credentials{}, credentialFormatError()
	}
	for _, name := range []string{"credentials", "DEVICE_SESSION_CREDENTIALS", "session"} {
		if nested, ok := object[name]; ok {
			var encoded string
			if json.Unmarshal(nested, &encoded) == nil {
				nested = []byte(encoded)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(nested, &fields); err != nil || fields == nil {
				return config.Credentials{}, credentialFormatError()
			}
			object = fields
		}
	}
	read := func(keys ...string) string {
		for _, key := range keys {
			var value string
			if json.Unmarshal(object[key], &value) == nil && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
		return ""
	}
	credentials := config.Credentials{AuthToken: read("authToken"), PartnerUserID: read("partnerUserID", "autoGeneratedLogin"), PartnerUserSecret: read("partnerUserSecret", "autoGeneratedPassword"), Email: read("email")}
	if raw, ok := object["accountID"]; ok {
		_ = json.Unmarshal(raw, &credentials.AccountID)
	}
	if err := validateCredentials(credentials); err != nil {
		return config.Credentials{}, err
	}
	return credentials, nil
}

func parseCredentialsContext(ctx context.Context, reader io.Reader) (config.Credentials, error) {
	type result struct {
		credentials config.Credentials
		err         error
	}
	done := make(chan result, 1)
	go func() {
		credentials, err := parseCredentials(reader)
		done <- result{credentials, err}
	}()
	select {
	case value := <-done:
		return value.credentials, value.err
	case <-ctx.Done():
		return config.Credentials{}, ctx.Err()
	}
}

func validateCredentials(credentials config.Credentials) error {
	if (credentials.PartnerUserID == "") != (credentials.PartnerUserSecret == "") {
		return fault.New("AUTH-002", "Device credentials are incomplete.", "Provide both partnerUserID and partnerUserSecret, or autoGeneratedLogin and autoGeneratedPassword.", 3)
	}
	if credentials.AuthToken == "" && credentials.PartnerUserID == "" {
		return credentialFormatError()
	}
	return nil
}

func credentialFormatError() error {
	return fault.New("AUTH-006", "Credential input is not supported JSON.", "Provide authToken and/or a device login and secret. Run expensify help auth for accepted field names.", 3)
}
