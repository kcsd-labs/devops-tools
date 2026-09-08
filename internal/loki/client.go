// Package loki is a minimal client for the Loki HTTP API.
//
// It provides what the kubelet cannot: logs from an arbitrary time range,
// including lines from pods that no longer exist.
package loki

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Entry is one log line: its timestamp, the text, and the pod it came from
// (needed when several pods are merged into one stream).
type Entry struct {
	TS   time.Time
	Line string
	Pod  string
}

type Client struct {
	base   string
	tenant string // X-Scope-OrgID; empty for a single-tenant Loki
	http   *http.Client
}

// New builds the client. Leave tenant empty unless Loki runs multi-tenant.
func New(base, tenant string) *Client {
	return &Client{
		base:   strings.TrimRight(base, "/"),
		tenant: tenant,
		http:   &http.Client{Timeout: 30 * time.Second},
	}
}

// QueryRange returns one page of results, oldest line first.
//
// With direction=backward Loki returns the newest lines within [start, end),
// which is what you want on screen first. Paging further back is driven by the
// caller moving `end` to the oldest line it already has, so neither side has to
// hold the whole range in memory.
func (c *Client) QueryRange(ctx context.Context, logql string, start, end time.Time, limit int, direction string) ([]Entry, error) {
	if direction != "forward" {
		direction = "backward"
	}
	q := url.Values{}
	q.Set("query", logql)
	q.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	q.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	q.Set("direction", direction)
	if limit > 0 { // otherwise Loki applies its own default
		q.Set("limit", strconv.Itoa(limit))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/loki/api/v1/query_range?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if c.tenant != "" {
		req.Header.Set("X-Scope-OrgID", c.tenant)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("loki status %d", resp.StatusCode)
	}

	var lr struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Stream map[string]string `json:"stream"`
				Values [][2]string       `json:"values"` // [ "<unix-nanos>", "<line>" ]
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return nil, err
	}
	if lr.Status != "success" {
		return nil, fmt.Errorf("loki status: %s", lr.Status)
	}

	var out []Entry
	for _, s := range lr.Data.Result {
		pod := s.Stream["pod"]
		for _, v := range s.Values {
			nsec, _ := strconv.ParseInt(v[0], 10, 64)
			out = append(out, Entry{TS: time.Unix(0, nsec), Line: v[1], Pod: pod})
		}
	}
	// Loki answers per stream; merge them into one chronological list.
	sort.Slice(out, func(i, j int) bool { return out[i].TS.Before(out[j].TS) })
	return out, nil
}
