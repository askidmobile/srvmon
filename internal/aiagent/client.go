package aiagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Event is one line of the NDJSON stream srvmon-ai sends for a run:
//
//	{"t":"text","d":"..."}                     assistant text delta
//	{"t":"tool","id":"..","name":"..","args":{}} tool call started
//	{"t":"tool_end","id":"..","ok":true,"out":"..."}
//	{"t":"error","msg":"..."}
//	{"t":"done","text":"..."}                  final assistant text
type Event struct {
	T    string          `json:"t"`
	D    string          `json:"d,omitempty"`
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
	OK   bool            `json:"ok,omitempty"`
	Out  string          `json:"out,omitempty"`
	Msg  string          `json:"msg,omitempty"`
	Text string          `json:"text,omitempty"`
}

// Status is what srvmon-ai reports about itself.
type Status struct {
	Ready    bool   `json:"ready"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Error    string `json:"error,omitempty"`
	Version  string `json:"version,omitempty"`
}

// RunRequest starts a chat turn or a task run in srvmon-ai.
type RunRequest struct {
	Text string `json:"text"`
	// Context tells the agent which server the chat is about.
	Server string `json:"server,omitempty"`
	// TaskID marks a scheduled run: srvmon-ai uses a throwaway session and
	// passes the id back on tool calls so confirmations name the task.
	TaskID int64 `json:"task_id,omitempty"`
}

// Client talks to the srvmon-ai container. Both directions authenticate
// with the same shared bearer token (SRVMON_AI_TOKEN).
type Client struct {
	base  string
	token string
	hc    *http.Client
}

// NewClient returns nil when the agent is not configured.
func NewClient(base, token string) *Client {
	if base == "" || token == "" {
		return nil
	}
	return &Client{base: strings.TrimRight(base, "/"), token: token, hc: &http.Client{}}
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(msg, &e) == nil && e.Error != "" {
			return nil, &HTTPError{Status: resp.StatusCode, Code: e.Error}
		}
		return nil, &HTTPError{Status: resp.StatusCode, Code: strings.TrimSpace(string(msg))}
	}
	return resp, nil
}

// HTTPError is a non-2xx answer from srvmon-ai.
type HTTPError struct {
	Status int
	Code   string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("srvmon-ai: %d %s", e.Status, e.Code) }

// IsBusy reports whether srvmon-ai refused a run because the chat is busy.
func IsBusy(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status == http.StatusConflict
}

// Status asks srvmon-ai whether a model is configured.
func (c *Client) Status(ctx context.Context) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := c.do(ctx, http.MethodGet, "/v1/status", nil)
	if err != nil {
		return Status{}, err
	}
	defer resp.Body.Close()
	var s Status
	err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&s)
	return s, err
}

// Run starts a chat turn (chatID > 0) or a task run (chatID == 0) and calls
// fn for every event until the stream ends. Cancelling ctx aborts the run.
func (c *Client) Run(ctx context.Context, chatID int64, req RunRequest, fn func(Event)) error {
	path := "/v1/run"
	if chatID > 0 {
		path = fmt.Sprintf("/v1/chats/%d/run", chatID)
	}
	resp, err := c.do(ctx, http.MethodPost, path, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	sawEnd := false
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev Event
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.T == "done" || ev.T == "error" {
			sawEnd = true
		}
		fn(ev)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if !sawEnd {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("srvmon-ai оборвал ответ")
	}
	return nil
}

// ForgetChat drops the model-side history of a chat.
func (c *Client) ForgetChat(ctx context.Context, chatID int64) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/v1/chats/%d", chatID), nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
