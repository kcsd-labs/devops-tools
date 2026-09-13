// Package audit records user actions as structured logs plus a Prometheus
// counter. There is no separate audit file: log shipping and Prometheus are
// expected to collect both.
package audit

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// operationsTotal counts operations by environment, namespace, operation and
// outcome. Prometheus answers "what happened and how often"; the details of a
// single action (who, which pod) live in the logs.
var operationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	// Underscores, not the hyphen of the project name: Prometheus metric names
	// are [a-zA-Z_:][a-zA-Z0-9_:]* and a hyphen makes registration panic at
	// startup.
	Name: "devops_tools_operations_total",
	Help: "Number of operations performed, by outcome.",
}, []string{"environment", "namespace", "operation", "allowed", "success"})

// Entry is a single recorded action.
type Entry struct {
	Time        time.Time
	User        string
	Environment string
	Namespace   string
	Operation   string
	Target      string // pod, release or secret name
	Allowed     bool
	Success     bool
	Error       string
}

// Recorder writes actions to the log and the metrics registry.
type Recorder struct{}

func New() *Recorder { return &Recorder{} }

// Log records one action.
func (r *Recorder) Log(e Entry) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	slog.Info(actionMsg,
		"user", e.User, "environment", e.Environment, "namespace", e.Namespace,
		"operation", e.Operation, "target", e.Target,
		"allowed", e.Allowed, "success", e.Success, "error", e.Error)

	operationsTotal.WithLabelValues(
		e.Environment, e.Namespace, e.Operation,
		strconv.FormatBool(e.Allowed), strconv.FormatBool(e.Success),
	).Inc()
}

// actionMsg is the message every recorded action carries. Reading the trail
// back means picking these lines out of everything else the service logs.
const actionMsg = "action"

// Parse reads back a line written by Log.
//
// Anything that is not an action is rejected — ordinary logs, a line from
// another container, a half-written record — so the caller can point this at a
// whole stdout stream without filtering first.
func Parse(line []byte) (Entry, bool) {
	var raw struct {
		Time        time.Time `json:"time"`
		Msg         string    `json:"msg"`
		User        string    `json:"user"`
		Environment string    `json:"environment"`
		Namespace   string    `json:"namespace"`
		Operation   string    `json:"operation"`
		Target      string    `json:"target"`
		Allowed     bool      `json:"allowed"`
		Success     bool      `json:"success"`
		Error       string    `json:"error"`
	}
	if err := json.Unmarshal(line, &raw); err != nil || raw.Msg != actionMsg {
		return Entry{}, false
	}
	return Entry{
		Time: raw.Time, User: raw.User, Environment: raw.Environment,
		Namespace: raw.Namespace, Operation: raw.Operation, Target: raw.Target,
		Allowed: raw.Allowed, Success: raw.Success, Error: raw.Error,
	}, true
}
