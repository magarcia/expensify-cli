// Package api implements the New Expensify web API and device-session renewal.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/magarcia/expensify-cli/internal/config"
	"github.com/magarcia/expensify-cli/internal/fault"
)

const (
	defaultBaseURL   = "https://www.expensify.com/api"
	maxResponseBytes = 32 << 20
	maxUploadBytes   = 32 << 20
	partnerName      = "chat-expensify-com"
	partnerPassword  = "e21965746fd75f82bb66"
)

// Options configures a client. SaveCredentials runs after successful renewal.
type Options struct {
	BaseURL         string
	HTTPClient      *http.Client
	Credentials     config.Credentials
	SaveCredentials func(config.Credentials) error
}

type Client struct {
	baseURL         *url.URL
	httpClient      *http.Client
	mu              sync.RWMutex
	credentials     config.Credentials
	refreshMu       sync.Mutex
	saveCredentials func(config.Credentials) error
}

// New validates the destination before credentials can be sent to it.
func New(options Options) (*Client, error) {
	base := options.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || u == nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" {
		return nil, invalidBaseURL()
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return nil, invalidBaseURL()
	}
	u.Path = strings.TrimRight(u.Path, "/")
	httpClient := &http.Client{}
	if options.HTTPClient != nil {
		*httpClient = *options.HTTPClient
	}
	if httpClient.Transport == nil {
		if transport, ok := http.DefaultTransport.(*http.Transport); ok {
			transport = transport.Clone()
			transport.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
			transport.TLSHandshakeTimeout = 5 * time.Second
			httpClient.Transport = transport
		} else {
			httpClient.Transport = http.DefaultTransport
		}
	}
	if httpClient.Timeout <= 0 {
		httpClient.Timeout = 30 * time.Second
	}
	// Sessions use form credentials; never follow a redirect or inherit cookies.
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	httpClient.Jar = nil
	return &Client{baseURL: u, httpClient: httpClient, credentials: options.Credentials, saveCredentials: options.SaveCredentials}, nil
}

func invalidBaseURL() error {
	return fault.New("CONFIG-003", "The API URL is invalid or insecure.", "Use an HTTPS URL. HTTP is allowed only for localhost development.", 2)
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Credentials returns a snapshot of the current session, including renewals.
func (c *Client) Credentials() config.Credentials {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.credentials
}

// Call sends one command. An expired session is renewed and replayed once.
// Transport errors and server errors are never retried, since writes may succeed
// before a connection is lost.
func (c *Client) Call(ctx context.Context, command string, params url.Values) (map[string]any, error) {
	return c.execute(ctx, command, func(token string) (map[string]any, int, error) {
		return c.request(ctx, command, params, token)
	})
}

func (c *Client) execute(ctx context.Context, command string, request func(string) (map[string]any, int, error)) (map[string]any, error) {
	if !validCommand(command) {
		return nil, fault.Usage("The API command must contain only letters and numbers, starting with a letter.")
	}
	if c.Credentials().AuthToken == "" {
		if err := c.renew(ctx, ""); err != nil {
			return nil, err
		}
	}
	credentials := c.Credentials()
	response, code, err := request(credentials.AuthToken)
	if err != nil {
		return nil, err
	}
	if code == 407 && command != "Authenticate" {
		if err := c.renew(ctx, credentials.AuthToken); err != nil {
			return nil, err
		}
		response, code, err = request(c.Credentials().AuthToken)
		if err != nil {
			return nil, err
		}
	}
	if code != 200 {
		return nil, apiError(code)
	}
	return response, nil
}

// Upload sends a JPEG, PNG, or PDF receipt as streaming multipart data. A file
// is reopened on session renewal, and changes between attempts are rejected.
func (c *Client) Upload(ctx context.Context, command string, params url.Values, field, filePath string) (map[string]any, error) {
	if !validCommand(field) {
		return nil, fault.Usage("The upload field must contain only letters and numbers, starting with a letter.")
	}
	file, info, contentType, err := openReceipt(filePath)
	if err != nil {
		return nil, err
	}
	file.Close()
	return c.execute(ctx, command, func(token string) (map[string]any, int, error) {
		file, current, currentType, err := openReceipt(filePath)
		if err != nil {
			return nil, 0, err
		}
		defer file.Close()
		if !os.SameFile(info, current) || info.Size() != current.Size() || !info.ModTime().Equal(current.ModTime()) || contentType != currentType {
			return nil, 0, fault.New("FILE-002", "The receipt changed before it could be uploaded.", "Retry with a stable receipt file.", 2)
		}
		var envelope bytes.Buffer
		writer := multipart.NewWriter(&envelope)
		form := requestForm(params, token)
		keys := make([]string, 0, len(form))
		for key := range form {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			for _, value := range form[key] {
				if err := writer.WriteField(key, value); err != nil {
					return nil, 0, invalidUpload()
				}
			}
		}
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": field, "filename": filepath.Base(filePath)}))
		header.Set("Content-Type", contentType)
		if _, err := writer.CreatePart(header); err != nil {
			return nil, 0, invalidUpload()
		}
		prefixSize := envelope.Len()
		if err := writer.Close(); err != nil {
			return nil, 0, invalidUpload()
		}
		body := io.MultiReader(bytes.NewReader(envelope.Bytes()[:prefixSize]), io.NewSectionReader(file, 0, info.Size()), bytes.NewReader(envelope.Bytes()[prefixSize:]))
		return c.send(ctx, command, body, writer.FormDataContentType(), int64(envelope.Len())+info.Size())
	})
}

func invalidUpload() error {
	return fault.New("FILE-001", "The receipt could not be read.", "Choose a readable JPEG, PNG, or PDF file of at most 32 MiB.", 2)
}

func openReceipt(path string) (*os.File, os.FileInfo, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, "", invalidUpload()
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxUploadBytes {
		file.Close()
		return nil, nil, "", invalidUpload()
	}
	header := make([]byte, 512)
	n, err := file.ReadAt(header, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		file.Close()
		return nil, nil, "", invalidUpload()
	}
	contentType := http.DetectContentType(header[:n])
	extension := strings.ToLower(filepath.Ext(path))
	valid := (extension == ".jpg" || extension == ".jpeg") && contentType == "image/jpeg" || extension == ".png" && contentType == "image/png" || extension == ".pdf" && contentType == "application/pdf"
	if !valid {
		file.Close()
		return nil, nil, "", invalidUpload()
	}
	return file, info, contentType, nil
}

func validCommand(command string) bool {
	if command == "" {
		return false
	}
	for i, ch := range command {
		if ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || i > 0 && ch >= '0' && ch <= '9' {
			continue
		}
		return false
	}
	return true
}

func (c *Client) renew(ctx context.Context, expiredToken string) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	if current := c.Credentials().AuthToken; current != "" && current != expiredToken {
		return nil
	}
	_, err := c.authenticate(ctx)
	return err
}

// Authenticate exchanges stored device credentials for a fresh session token.
func (c *Client) Authenticate(ctx context.Context) (config.Credentials, error) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	return c.authenticate(ctx)
}

func (c *Client) authenticate(ctx context.Context) (config.Credentials, error) {
	credentials := c.Credentials()
	if credentials.PartnerUserID == "" || credentials.PartnerUserSecret == "" {
		return config.Credentials{}, fault.New("AUTH-001", "Device credentials are required to renew this session.", "Run expensify auth login with New Expensify device credentials.", 3)
	}
	params := url.Values{
		"partnerName":       {partnerName},
		"partnerPassword":   {partnerPassword},
		"partnerUserID":     {credentials.PartnerUserID},
		"partnerUserSecret": {credentials.PartnerUserSecret},
		"useExpensifyLogin": {"false"},
	}
	response, code, err := c.request(ctx, "Authenticate", params, "")
	if err != nil {
		return config.Credentials{}, err
	}
	if code != 200 {
		return config.Credentials{}, apiError(code)
	}
	token, _ := response["authToken"].(string)
	if strings.TrimSpace(token) == "" {
		return config.Credentials{}, fault.New("AUTH-004", "Expensify returned no session token.", "Try logging in again with fresh device credentials.", 3)
	}
	credentials.AuthToken = token
	if email, ok := response["email"].(string); ok && email != "" {
		credentials.Email = email
	}
	if accountID, ok := response["accountID"].(json.Number); ok {
		if id, err := accountID.Int64(); err == nil && id > 0 {
			credentials.AccountID = id
		}
	}
	c.mu.Lock()
	c.credentials = credentials
	c.mu.Unlock()
	if c.saveCredentials != nil {
		if err := c.saveCredentials(credentials); err != nil {
			return config.Credentials{}, fault.New("AUTH-005", "The new session could not be saved.", "Unlock or enable your OS keyring, or check private credential-file permissions, then log in again.", 5)
		}
	}
	return credentials, nil
}

func requestForm(params url.Values, token string) url.Values {
	form := make(url.Values, len(params)+5)
	for key, values := range params {
		form[key] = append([]string(nil), values...)
	}
	form.Set("platform", "web")
	form.Set("referer", "ecash")
	form.Set("api_setCookie", "false")
	form.Set("doNotRetry", "true")
	form.Set("authToken", token)
	return form
}

func (c *Client) request(ctx context.Context, command string, params url.Values, token string) (map[string]any, int, error) {
	encoded := requestForm(params, token).Encode()
	return c.send(ctx, command, strings.NewReader(encoded), "application/x-www-form-urlencoded", int64(len(encoded)))
}

func (c *Client) send(ctx context.Context, command string, body io.Reader, contentType string, contentLength int64) (map[string]any, int, error) {
	u := *c.baseURL
	u.Path += "/" + command
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), body)
	if err != nil {
		return nil, 0, fault.New("NET-001", "The API request could not be created.", "Check the API URL and try again.", 10)
	}
	request.ContentLength = contentLength
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "expensify-cli")
	response, err := c.httpClient.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return nil, 0, fault.New("NET-003", "The request was canceled.", "Run the command again when ready.", 10)
		}
		var netError net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netError) && netError.Timeout() {
			return nil, 0, fault.New("NET-002", "The Expensify request timed out.", "Check connectivity. Before retrying a write, check whether it was applied.", 10)
		}
		return nil, 0, fault.New("NET-001", "Could not connect to Expensify.", "Check connectivity. Before retrying a write, check whether it was applied.", 10)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound {
		return nil, response.StatusCode, nil
	}
	if response.StatusCode == http.StatusTooManyRequests {
		return nil, 0, apiError(http.StatusTooManyRequests)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, 0, fault.New("API-HTTP", "Expensify returned an unsuccessful HTTP response.", "Try again later. Before retrying a write, check whether it was applied.", 1)
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, 0, invalidResponse()
	}
	if len(responseBody) > maxResponseBytes {
		return nil, 0, fault.New("API-003", "The Expensify response exceeded the size limit.", "Request fewer records and try again.", 1)
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.UseNumber()
	var data map[string]any
	if err := decoder.Decode(&data); err != nil || data == nil {
		return nil, 0, invalidResponse()
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, 0, invalidResponse()
	}
	code, err := strconv.Atoi(stringValue(data["jsonCode"]))
	if err != nil {
		return nil, 0, invalidResponse()
	}
	return data, code, nil
}

func stringValue(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	default:
		return ""
	}
}

func invalidResponse() error {
	return fault.New("API-002", "Expensify returned an invalid response.", "Try again later. Before retrying a write, check whether it was applied.", 1)
}

func apiError(code int) error {
	switch code {
	case 401, 407:
		return fault.New("AUTH-002", "Expensify rejected the session credentials.", "Run expensify auth login with fresh New Expensify device credentials.", 3)
	case 403:
		return fault.New("AUTH-003", "This account is not allowed to perform that action.", "Check the account and workspace permissions.", 5)
	case 404:
		return fault.New("API-404", "Expensify could not find that resource.", "Check the resource ID and account access.", 4)
	case 429:
		return fault.New("API-429", "Expensify is rate limiting requests.", "Wait before trying again.", 10)
	default:
		return fault.New("API-001", "Expensify rejected the request (jsonCode "+strconv.Itoa(code)+").", "Check the command inputs and account permissions, then try again.", 1)
	}
}
