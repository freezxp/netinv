// Package flowstore keeps raw flow records in ClickHouse (ADR-026, doc 34 §7).
//
// The collector writes through it and the API reads through it. It speaks
// ClickHouse's HTTP interface directly rather than pulling in a driver: the
// operations needed are few (run DDL, insert a batch, run a parameterised
// select), and every value that comes from a request travels as a typed query
// parameter, never spliced into SQL text.
package flowstore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Database holds every NetInv flow table.
const Database = "netinv_flow"

// Client is a minimal ClickHouse HTTP client.
type Client struct {
	// URL is the HTTP endpoint, e.g. http://clickhouse:8123.
	URL      string
	User     string
	Password string
	HTTP     *http.Client
}

// NewClient returns a client with a bounded request timeout. A query that
// runs longer than the timeout is cancelled server-side as well, through
// max_execution_time, so an abandoned explorer request does not keep a core
// busy.
func NewClient(rawURL, user, password string) *Client {
	return &Client{
		URL: strings.TrimRight(rawURL, "/"), User: user, Password: password,
		HTTP: &http.Client{Timeout: 60 * time.Second},
	}
}

// Error is a ClickHouse error response, kept whole for the log and cut short
// for anything shown to a user.
type Error struct {
	Status int
	Body   string
}

func (e *Error) Error() string {
	b := e.Body
	if len(b) > 300 {
		b = b[:300] + "…"
	}
	return fmt.Sprintf("clickhouse: HTTP %d: %s", e.Status, strings.TrimSpace(b))
}

// Exec runs a statement that returns nothing worth reading (DDL, ALTER).
func (c *Client) Exec(ctx context.Context, sql string, params map[string]string) error {
	resp, err := c.do(ctx, sql, params, nil, nil)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// Insert sends rows already encoded as JSONEachRow. settings carry
// per-insert options such as the deduplication token.
func (c *Client) Insert(ctx context.Context, table string, body []byte, settings map[string]string) error {
	resp, err := c.do(ctx, "INSERT INTO "+table+" FORMAT JSONEachRow", nil, settings, body)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// Select runs a query and decodes each JSONEachRow line with UseNumber, so
// 64-bit counters arrive intact rather than rounded through float64.
func (c *Client) Select(ctx context.Context, sql string, params map[string]string) ([]map[string]any, error) {
	resp, err := c.do(ctx, sql+" FORMAT JSONEachRow", params, map[string]string{
		"output_format_json_quote_64bit_integers": "0",
		"max_execution_time":                      "30",
		// A runaway explorer query is refused rather than allowed to take the
		// box's memory with it; 2 GB is far above what a sane group-by needs.
		"max_memory_usage": "2000000000",
		"readonly":         "2",
		// ClickHouse lets a SELECT alias shadow a column in WHERE, so
		// "replaceRegexpOne(...) AS src_addr" would turn the src_addr in a
		// filter into a string compared against an IPv6. Columns win.
		"prefer_column_name_to_alias": "1",
	}, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []map[string]any
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		row := map[string]any{}
		if err := dec.Decode(&row); err != nil {
			return nil, fmt.Errorf("clickhouse: decode row: %w", err)
		}
		out = append(out, row)
	}
	return out, sc.Err()
}

// Ping reports whether the server answers.
func (c *Client) Ping(ctx context.Context) error {
	return c.Exec(ctx, "SELECT 1", nil)
}

func (c *Client) do(ctx context.Context, sql string, params, settings map[string]string, body []byte) (*http.Response, error) {
	q := url.Values{}
	for k, v := range params {
		q.Set("param_"+k, v)
	}
	for k, v := range settings {
		q.Set(k, v)
	}
	var rd io.Reader
	if body != nil {
		// The statement goes in the URL and the rows in the body: that is how
		// ClickHouse's HTTP interface tells an INSERT's data from its text.
		q.Set("query", sql)
		rd = bytes.NewReader(body)
	} else {
		rd = strings.NewReader(sql)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/?"+q.Encode(), rd)
	if err != nil {
		return nil, err
	}
	if c.User != "" {
		req.Header.Set("X-ClickHouse-User", c.User)
		req.Header.Set("X-ClickHouse-Key", c.Password)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, &Error{Status: resp.StatusCode, Body: string(b)}
	}
	return resp, nil
}
