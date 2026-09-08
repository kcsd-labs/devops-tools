// Package audit records user actions as structured logs plus a Prometheus
// counter. There is no separate audit file: log shipping and Prometheus are
// expected to collect both.
package audit

import (
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
	slog.Info("action",
		"user", e.User, "environment", e.Environment, "namespace", e.Namespace,
		"operation", e.Operation, "target", e.Target,
		"allowed", e.Allowed, "success", e.Success, "error", e.Error)

	operationsTotal.WithLabelValues(
		e.Environment, e.Namespace, e.Operation,
		strconv.FormatBool(e.Allowed), strconv.FormatBool(e.Success),
	).Inc()
}
