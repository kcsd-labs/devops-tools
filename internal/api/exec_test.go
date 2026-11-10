package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/tools/remotecommand"

	"devops-tools/internal/audit"
	"devops-tools/internal/auth"
	"devops-tools/internal/config"
)

// events is one timeline of what the trail recorded and what the shell
// received, so the test can say which came first.
type events struct {
	mu   sync.Mutex
	list []string
}

func (e *events) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, s)
}

func (e *events) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.list...)
}

// trailHandler puts each audit record on the timeline.
type trailHandler struct{ ev *events }

func (h trailHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h trailHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h trailHandler) WithGroup(string) slog.Handler            { return h }
func (h trailHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "action" {
		return nil
	}
	var op, detail string
	ok := true
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "operation":
			op = a.Value.String()
		case "detail":
			detail = a.Value.String()
		case "success":
			ok = a.Value.Bool()
		}
		return true
	})
	if !ok {
		op += "(refused)"
	}
	h.ev.add("trail " + op + " " + detail)
	return nil
}

// fakeShell records each line as the shell receives it, and echoes it.
func fakeShell(ev *events) shellOpener {
	return func(ctx context.Context, _, _, _, _, _ string, stdin io.Reader, stdout io.Writer,
		_ remotecommand.TerminalSizeQueue) error {
		r := bufio.NewReader(stdin)
		for {
			b, err := r.ReadByte()
			if err != nil {
				return nil
			}
			switch b {
			case 0x03:
				ev.add("shell ^C")
			case 0x04:
				ev.add("shell ^D")
				return nil
			default:
				line, _ := r.ReadString('\r')
				line = string(b) + strings.TrimSuffix(line, "\r")
				ev.add("shell " + line)
				_, _ = stdout.Write([]byte("out: " + line + "\r\n"))
			}
		}
	}
}

func shellServer(t *testing.T, ev *events, idle time.Duration) (*httptest.Server, func()) {
	prev := slog.Default()
	slog.SetDefault(slog.New(trailHandler{ev}))
	s := &Server{
		cfg: &config.Config{Environment: "test", Exec: config.ExecConfig{
			IdleTimeout: idle, MaxDuration: time.Minute, MaxSessionsPerUser: 2,
		}},
		audit:     audit.New(),
		openShell: fakeShell(ev),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		x := &shellSession{s: s, conn: conn, id: "t1", started: time.Now(),
			t:     auth.Ticket{User: "anna", Namespace: "abs-dev", Pod: "api-1", Container: "app", Shell: "/bin/sh"},
			sizes: make(chan remotecommand.TerminalSize, 4)}
		x.run(r.Context(), time.Now().Add(time.Minute))
	}))
	return srv, func() { srv.Close(); slog.SetDefault(prev) }
}

func dial(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func sendMsg(t *testing.T, c *websocket.Conn, m execMessage) {
	t.Helper()
	b, _ := json.Marshal(m)
	if err := c.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatal(err)
	}
}

// readUntil reads messages until one of the given type, returning the status
// messages seen and the terminal output.
func readUntil(t *testing.T, c *websocket.Conn, kind string) (string, map[string]any) {
	t.Helper()
	var out strings.Builder
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		mt, data, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("waiting for %q: %v (output so far %q)", kind, err, out.String())
		}
		if mt == websocket.BinaryMessage {
			out.Write(data)
			continue
		}
		var st map[string]any
		_ = json.Unmarshal(data, &st)
		if st["type"] == kind {
			return out.String(), st
		}
	}
}

func waitFor(t *testing.T, ev *events, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range ev.snapshot() {
			if e == want {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never saw %q in %q", want, ev.snapshot())
}

// The point of the mirror: every line is on record before the shell has it,
// a line that would not run as recorded never reaches the shell, and the
// session's opening and end are on record too.
func TestEachLineIsRecordedBeforeTheShellGetsIt(t *testing.T) {
	ev := &events{}
	srv, done := shellServer(t, ev, time.Minute)
	defer done()
	c := dial(t, srv)
	defer c.Close()

	readUntil(t, c, "ready")
	sendMsg(t, c, execMessage{Type: "line", Text: "ls -la"})
	sendMsg(t, c, execMessage{Type: "line", Text: "ls\t"}) // Tab would complete: refused
	_, refused := readUntil(t, c, "refused")
	if !strings.Contains(refused["message"].(string), "control character") {
		t.Errorf("refusal says %q", refused["message"])
	}
	sendMsg(t, c, execMessage{Type: "line", Text: "PGPASSWORD=hunter2 psql"})
	sendMsg(t, c, execMessage{Type: "key", Key: "c"})
	sendMsg(t, c, execMessage{Type: "key", Key: "d"})
	_, closed := readUntil(t, c, "closed")
	if closed["reason"] != "the shell exited" {
		t.Errorf("closed because %q", closed["reason"])
	}

	got := ev.snapshot()
	at := func(s string) int {
		for i, e := range got {
			if e == s {
				return i
			}
		}
		return -1
	}
	whole := "\nwhole timeline:\n" + strings.Join(got, "\n")

	// Each record comes before the shell has what it records. When the shell
	// reads a line relative to the next record is the shell's business, so
	// only these pairs are fixed.
	for _, pair := range [][2]string{
		{"trail pod-exec shell /bin/sh", "trail pod-exec-command ls -la"},
		{"trail pod-exec-command ls -la", "shell ls -la"},
		{"trail pod-exec-command PGPASSWORD=*** psql", "shell PGPASSWORD=hunter2 psql"},
		{"trail pod-exec-key ^C", "shell ^C"},
		{"trail pod-exec-key ^D", "shell ^D"},
	} {
		first, then := at(pair[0]), at(pair[1])
		if first < 0 || then < 0 || first > then {
			t.Errorf("want %q before %q%s", pair[0], pair[1], whole)
		}
	}
	// Refused: on record, and never in the shell.
	if at("trail pod-exec-command(refused) ls\t") < 0 {
		t.Errorf("the refused line is not on record%s", whole)
	}
	for _, e := range got {
		if strings.HasPrefix(e, "shell ls\t") {
			t.Errorf("a refused line reached the shell%s", whole)
		}
	}
	// The password reaches the shell, not the trail.
	for _, e := range got {
		if strings.HasPrefix(e, "trail") && strings.Contains(e, "hunter2") {
			t.Errorf("the trail holds the password: %q", e)
		}
	}
	if last := got[len(got)-1]; !strings.HasPrefix(last, "trail pod-exec-end the shell exited after") ||
		!strings.HasSuffix(last, ", 3 lines") {
		t.Errorf("end record = %q%s", last, whole)
	}
}

// Nobody at the keyboard: the session closes, and says why.
func TestAnIdleSessionIsClosed(t *testing.T) {
	ev := &events{}
	srv, done := shellServer(t, ev, 200*time.Millisecond)
	defer done()
	c := dial(t, srv)
	defer c.Close()

	readUntil(t, c, "ready")
	_, closed := readUntil(t, c, "closed")
	if !strings.HasPrefix(closed["reason"].(string), "idle for") {
		t.Errorf("closed because %q", closed["reason"])
	}
	waitFor(t, ev, "trail pod-exec-end idle for 200ms after 0s, 0 lines")
}

func TestShellLimitsPerPerson(t *testing.T) {
	var l shellLimits
	if !l.acquire("anna", 2) || !l.acquire("anna", 2) {
		t.Fatal("two shells should be allowed")
	}
	if l.acquire("anna", 2) {
		t.Error("a third should be refused")
	}
	if !l.acquire("boris", 2) {
		t.Error("another person's count is their own")
	}
	l.release("anna")
	if !l.acquire("anna", 2) {
		t.Error("closing one should free a place")
	}
}
