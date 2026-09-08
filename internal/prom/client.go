// Package prom is a minimal client for the Prometheus HTTP API.
package prom

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Point is one sample of a time series.
type Point struct {
	T float64 `json:"t"` // unix seconds
	V float64 `json:"v"`
}

type Client struct {
	base string
	http *http.Client
}

func New(base string) *Client {
	return &Client{
		base: strings.TrimRight(base, "/"),
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

// QueryRange runs a range query and returns the first series. Every query we
// send aggregates with sum(), so there is only ever one. Returns nil when the
// query matched nothing.
func (c *Client) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]Point, error) {
	q := url.Values{}
	q.Set("query", query)
	q.Set("start", strconv.FormatInt(start.Unix(), 10))
	q.Set("end", strconv.FormatInt(end.Unix(), 10))
	q.Set("step", strconv.Itoa(int(step.Seconds()))+"s")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/api/v1/query_range?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus status %d", resp.StatusCode)
	}

	var pr struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Values [][2]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, err
	}
	if pr.Status != "success" || len(pr.Data.Result) == 0 {
		return nil, nil
	}

	vals := pr.Data.Result[0].Values
	out := make([]Point, 0, len(vals))
	for _, v := range vals {
		var ts float64
		var sv string
		_ = json.Unmarshal(v[0], &ts)
		_ = json.Unmarshal(v[1], &sv)
		f, _ := strconv.ParseFloat(sv, 64)
		out = append(out, Point{T: ts, V: f})
	}
	return out, nil
}
