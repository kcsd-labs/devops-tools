package audit

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// The detail has to make the whole trip: written with the action, read back
// from the line. Losing it on either side would look like nothing was recorded.
func TestTheDetailIsWrittenAndReadBack(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	New().Log(Entry{
		User: "ada", Namespace: "payments-dev", Operation: "secret-update",
		Target: "payments-api", Allowed: true, Success: true,
		Detail: "added KAFKA_PASSWORD; changed DB_URL",
	})

	got, ok := Parse(bytes.TrimSpace(buf.Bytes()))
	if !ok {
		t.Fatalf("the line written was not read back as an action: %s", buf.String())
	}
	if got.Detail != "added KAFKA_PASSWORD; changed DB_URL" {
		t.Errorf("Detail = %q", got.Detail)
	}
	if got.Target != "payments-api" || got.User != "ada" {
		t.Errorf("the rest of the entry did not survive: %+v", got)
	}
}

// Every line in Loki today was written before the field existed. They have to
// keep reading as they did, with the detail simply empty.
func TestALineFromBeforeTheDetailStillReads(t *testing.T) {
	line := `{"time":"2026-10-22T14:10:37Z","level":"INFO","msg":"action","user":"grace",` +
		`"environment":"dev","namespace":"payments-dev","operation":"secret-update",` +
		`"target":"payments-api","allowed":true,"success":true,"error":""}`
	got, ok := Parse([]byte(line))
	if !ok {
		t.Fatal("an old line no longer reads as an action")
	}
	if got.Detail != "" || got.User != "grace" || !strings.HasPrefix(got.Operation, "secret") {
		t.Errorf("old line read as %+v", got)
	}
}
