package browserauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/coder/websocket"
	"github.com/magarcia/expensify-cli/internal/config"
)

type protocol interface {
	Call(context.Context, string, any, string, any) error
	Close()
}

type socket interface {
	Write(context.Context, websocket.MessageType, []byte) error
	Read(context.Context) (websocket.MessageType, []byte, error)
	CloseNow() error
}

type cdp struct {
	socket socket
	nextID int64
}

type protocolError struct{}

func (*protocolError) Error() string { return "browser command could not complete" }

func dialCDP(ctx context.Context, endpoint string) (protocol, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "ws" || parsed.Hostname() != "127.0.0.1" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid loopback debugging endpoint")
	}
	if _, err := parseEndpoint(parsed.Port() + "\n" + parsed.Path); err != nil {
		return nil, err
	}
	transport := &http.Transport{Proxy: nil}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("debugging redirects are not allowed")
		},
	}
	connection, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPClient: client})
	transport.CloseIdleConnections()
	if err != nil {
		return nil, errors.New("cannot connect to the sign-in browser")
	}
	connection.SetReadLimit(1 << 20)
	return &cdp{socket: connection}, nil
}

func (c *cdp) Close() { _ = c.socket.CloseNow() }

func (c *cdp) Call(ctx context.Context, method string, params any, session string, result any) error {
	c.nextID++
	request := struct {
		ID        int64  `json:"id"`
		Method    string `json:"method"`
		Params    any    `json:"params,omitempty"`
		SessionID string `json:"sessionId,omitempty"`
	}{c.nextID, method, params, session}
	data, err := json.Marshal(request)
	if err != nil {
		return errors.New("cannot encode browser command")
	}
	if err := c.socket.Write(ctx, websocket.MessageText, data); err != nil {
		return errors.New("cannot write browser command")
	}
	for {
		_, data, err := c.socket.Read(ctx)
		if err != nil {
			return errors.New("cannot read browser response")
		}
		var response struct {
			ID     int64           `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return errors.New("invalid browser response")
		}
		if response.ID != request.ID {
			continue
		}
		if len(response.Error) > 0 && string(response.Error) != "null" {
			return &protocolError{}
		}
		if result != nil && len(response.Result) > 0 {
			if err := json.Unmarshal(response.Result, result); err != nil {
				return errors.New("invalid browser command result")
			}
		}
		return nil
	}
}

type target struct {
	ID   string `json:"targetId"`
	Type string `json:"type"`
	URL  string `json:"url"`
}

func pollCredentials(ctx context.Context, connection protocol) (config.Credentials, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		var targets struct {
			Infos []target `json:"targetInfos"`
		}
		if err := connection.Call(ctx, "Target.getTargets", nil, "", &targets); err != nil {
			return config.Credentials{}, err
		}
		for _, page := range targets.Infos {
			if page.Type != "page" || !allowedPage(page.URL) {
				continue
			}
			credentials, err := targetCredentials(ctx, connection, page.ID)
			if err != nil {
				var navigation *protocolError
				if errors.As(err, &navigation) {
					continue
				}
				return config.Credentials{}, err
			}
			if completeCredentials(credentials) {
				return credentials, nil
			}
		}
		select {
		case <-ctx.Done():
			return config.Credentials{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func allowedPage(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.User == nil && parsed.Port() == "" && (parsed.Host == "new.expensify.com" || parsed.Host == "www.expensify.com")
}

func targetCredentials(ctx context.Context, connection protocol, targetID string) (config.Credentials, error) {
	var attached struct {
		Session string `json:"sessionId"`
	}
	if err := connection.Call(ctx, "Target.attachToTarget", map[string]any{"targetId": targetID, "flatten": true}, "", &attached); err != nil {
		return config.Credentials{}, err
	}
	defer func() {
		_ = connection.Call(ctx, "Target.detachFromTarget", map[string]string{"sessionId": attached.Session}, "", nil)
	}()
	var evaluated struct {
		Result struct {
			Value config.Credentials `json:"value"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	params := map[string]any{"expression": extractionJS, "awaitPromise": true, "returnByValue": true, "timeout": 3000}
	if err := connection.Call(ctx, "Runtime.evaluate", params, attached.Session, &evaluated); err != nil {
		return config.Credentials{}, err
	}
	if len(evaluated.Exception) > 0 && string(evaluated.Exception) != "null" {
		return config.Credentials{}, nil
	}
	return evaluated.Result.Value, nil
}

func completeCredentials(credentials config.Credentials) bool {
	return credentials.PartnerUserID != "" && credentials.PartnerUserSecret != "" && credentials.AuthToken != "" && credentials.Email != "" && credentials.AccountID > 0 && credentials.AccountID <= 1<<53-1 && len(credentials.PartnerUserID) <= 16384 && len(credentials.PartnerUserSecret) <= 16384 && len(credentials.AuthToken) <= 16384 && len(credentials.Email) <= 1024
}
