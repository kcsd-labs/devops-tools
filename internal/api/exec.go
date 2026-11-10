package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/remotecommand"

	"devops-tools/internal/audit"
	"devops-tools/internal/auth"
	"devops-tools/internal/kube"
	"devops-tools/internal/shell"
)

// A shell in a pod is a mirror. The browser never types into the container:
// it sends whole lines, and each one is written to the audit trail before it
// is passed on. A line the trail could not have described — one carrying a
// control character a line editor would act on — is refused. So the trail is
// what ran, not a reconstruction of it.

// Audit operations of a session. The opening is recorded under the permission
// itself, so that filtering the trail by pod-exec finds every session.
const (
	opExecCommand = "pod-exec-command"
	opExecKey     = "pod-exec-key"
	opExecEnd     = "pod-exec-end"
	ticketExec    = "exec"
	// How long the browser has to open the connection a ticket is for.
	execTicketTTL = 30 * time.Second
)

// shellOpener runs an interactive shell in a container until it exits or ctx
// ends.
type shellOpener func(ctx context.Context, asUser, namespace, pod, container, shellPath string,
	stdin io.Reader, stdout io.Writer, sizes remotecommand.TerminalSizeQueue) error

func (s *Server) opener() shellOpener {
	if s.openShell != nil {
		return s.openShell
	}
	return s.kube.OpenShell
}

// shellLimits counts the shells each person holds open on this replica.
type shellLimits struct {
	mu   sync.Mutex
	open map[string]int
}

func (l *shellLimits) acquire(user string, max int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open == nil {
		l.open = map[string]int{}
	}
	if l.open[user] >= max {
		return false
	}
	l.open[user]++
	return true
}

func (l *shellLimits) release(user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open[user] > 0 {
		l.open[user]--
	}
}

func (l *shellLimits) count(user string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.open[user]
}

// handleExecTicket checks that a shell may be opened in a container and hands
// back a ticket to open it with.
//
// Two steps because the second is a WebSocket, and a browser cannot put a
// bearer token on one. Everything that needs the person's own request — who
// they are, what they may do, when their sign-in ends — is settled here and
// signed into the ticket.
func (s *Server) handleExecTicket(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.FromContext(r.Context())
	namespace := chi.URLParam(r, "namespace")
	pod := chi.URLParam(r, "pod")
	var body struct {
		Container string `json:"container"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
	}

	refuse := func(err error, code int) {
		s.audit.Log(audit.Entry{
			User: user.Username, Environment: s.cfg.Environment, Namespace: namespace,
			Operation: OpPodExec, Target: target(pod, body.Container), Allowed: true,
			Success: false, Error: err.Error(),
		})
		http.Error(w, err.Error(), code)
	}

	t, err := s.kube.CheckExecTarget(r.Context(), user.Username, namespace, pod, body.Container)
	if err != nil {
		code := http.StatusUnprocessableEntity
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			code = kubeStatus(err)
		}
		refuse(err, code)
		return
	}
	sh, err := s.kube.FindShell(r.Context(), user.Username, namespace, pod, t.Container)
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, kube.ErrNoShell) {
			code = http.StatusUnprocessableEntity
		}
		refuse(err, code)
		return
	}
	if s.shells.count(user.Username) >= s.cfg.Exec.MaxSessionsPerUser {
		refuse(fmt.Errorf("you already have %d shells open — close one first", s.cfg.Exec.MaxSessionsPerUser),
			http.StatusTooManyRequests)
		return
	}

	until := time.Now().Add(s.cfg.Exec.MaxDuration)
	if exp := auth.TokenExpiry(r); !exp.IsZero() && exp.Before(until) {
		until = exp
	}
	ticket, err := s.auth.IssueTicket(auth.Ticket{
		Purpose: ticketExec, User: user.Username, Namespace: namespace, Pod: pod,
		Container: t.Container, Shell: sh, Until: until.Unix(),
	}, execTicketTTL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ticket":      ticket,
		"container":   t.Container,
		"shell":       sh,
		"idleSeconds": int(s.cfg.Exec.IdleTimeout.Seconds()),
		"until":       until.UnixMilli(),
	})
}

func target(pod, container string) string {
	if container == "" {
		return pod
	}
	return pod + "/" + container
}

// execMessage is what the browser sends: a line, a key, or a window size.
type execMessage struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Key  string `json:"key"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// handleExecSession runs the shell a ticket was issued for.
func (s *Server) handleExecSession(w http.ResponseWriter, r *http.Request) {
	t, err := s.auth.VerifyTicket(r.URL.Query().Get("ticket"), ticketExec)
	if err != nil || t.User == "" {
		http.Error(w, "invalid ticket", http.StatusUnauthorized)
		return
	}
	// Asked again rather than trusted from the ticket: it is short-lived, but
	// a role revoked in between should still be a door closed.
	if !s.authz.Allowed(s.auth.CurrentRoles(t.User), t.Namespace, OpPodExec) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	until := time.Unix(t.Until, 0)
	if !until.After(time.Now()) {
		http.Error(w, "the session this ticket was for has already ended", http.StatusUnauthorized)
		return
	}
	if !s.shells.acquire(t.User, s.cfg.Exec.MaxSessionsPerUser) {
		http.Error(w, "too many shells open", http.StatusTooManyRequests)
		return
	}
	defer s.shells.release(t.User)

	up := websocket.Upgrader{CheckOrigin: s.sameOrigin}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return // the upgrader has answered
	}
	defer conn.Close()

	sess := &shellSession{
		s: s, conn: conn, t: t, id: newSessionID(), started: time.Now(),
		sizes: make(chan remotecommand.TerminalSize, 4),
	}
	sess.run(r.Context(), until)
}

// sameOrigin accepts the page this service serves, and the development origin
// it is configured to answer. Without it any site the person has open could
// drive their shell with a ticket it never saw — the browser would add the
// connection to it without asking.
func (s *Server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Host == r.Host {
		return true
	}
	return s.cfg.Server.CORSOrigin != "" && origin == s.cfg.Server.CORSOrigin
}

func newSessionID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type shellSession struct {
	s       *Server
	conn    *websocket.Conn
	t       auth.Ticket
	id      string
	started time.Time
	sizes   chan remotecommand.TerminalSize

	writeMu sync.Mutex
	seq     int // every input, lines and keys
	lines   int
}

func (x *shellSession) entry(op, detail string) audit.Entry {
	return audit.Entry{
		User: x.t.User, Environment: x.s.cfg.Environment, Namespace: x.t.Namespace,
		Operation: op, Target: target(x.t.Pod, x.t.Container), Allowed: true, Success: true,
		Detail: detail, Session: x.id, Seq: x.seq,
	}
}

func (x *shellSession) send(kind int, data []byte) error {
	x.writeMu.Lock()
	defer x.writeMu.Unlock()
	_ = x.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return x.conn.WriteMessage(kind, data)
}

func (x *shellSession) status(v map[string]any) {
	b, _ := json.Marshal(v)
	_ = x.send(websocket.TextMessage, b)
}

// Write takes the terminal's output to the browser.
func (x *shellSession) Write(p []byte) (int, error) {
	if err := x.send(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Next is the terminal size queue the exec reads; nil ends it.
func (x *shellSession) Next() *remotecommand.TerminalSize {
	size, ok := <-x.sizes
	if !ok {
		return nil
	}
	return &size
}

func (x *shellSession) run(parent context.Context, until time.Time) {
	ctx, cancel := context.WithDeadline(context.WithoutCancel(parent), until)
	defer cancel()

	cfg := x.s.cfg.Exec
	x.s.audit.Log(x.entry(OpPodExec, "shell "+x.t.Shell))

	var (
		reasonMu sync.Mutex
		reason   string
	)
	end := func(why string) {
		reasonMu.Lock()
		if reason == "" {
			reason = why
		}
		reasonMu.Unlock()
		cancel()
	}

	stdin, stdinW := io.Pipe()
	shellDone := make(chan error, 1)
	go func() {
		shellDone <- x.s.opener()(ctx, x.t.User, x.t.Namespace, x.t.Pod, x.t.Container, x.t.Shell,
			stdin, x, x)
	}()

	x.status(map[string]any{
		"type": "ready", "session": x.id, "shell": x.t.Shell, "container": x.t.Container,
		"until": until.UnixMilli(), "idleSeconds": int(cfg.IdleTimeout.Seconds()),
	})

	// Idle: reset by what the person sends, not by what the shell prints — a
	// tail -f left running is not someone at the keyboard.
	idle := time.AfterFunc(cfg.IdleTimeout, func() { end("idle for " + cfg.IdleTimeout.String()) })
	defer idle.Stop()

	// A revoked role, or a namespace closed to shells since, ends the session
	// within the minute rather than at its deadline.
	go func() {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if !x.s.authz.Allowed(x.s.auth.CurrentRoles(x.t.User), x.t.Namespace, OpPodExec) {
					end("access was revoked")
					return
				}
				x.writeMu.Lock()
				_ = x.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
				x.writeMu.Unlock()
			}
		}
	}()

	// The browser's messages, read until it leaves or the session ends.
	msgs := make(chan execMessage)
	go func() {
		defer close(msgs)
		x.conn.SetReadLimit(shell.MaxLine + 1024)
		for {
			_, data, err := x.conn.ReadMessage()
			if err != nil {
				end("the browser disconnected")
				return
			}
			var m execMessage
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			select {
			case msgs <- m:
			case <-ctx.Done():
				return
			}
		}
	}()

loop:
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				end("the session reached its time limit")
			}
			break loop
		case err := <-shellDone:
			if err != nil {
				end("the shell ended: " + err.Error())
			} else {
				end("the shell exited")
			}
			shellDone <- err // put back for the wait below
			break loop
		case m, ok := <-msgs:
			if !ok {
				break loop
			}
			switch m.Type {
			case "line":
				idle.Reset(cfg.IdleTimeout)
				x.seq++
				x.lines++
				if err := shell.CheckLine(m.Text); err != nil {
					e := x.entry(opExecCommand, shell.Mask(m.Text))
					e.Success, e.Error = false, err.Error()
					x.s.audit.Log(e)
					x.status(map[string]any{"type": "refused", "message": err.Error()})
					continue
				}
				// The record first. Only a line on record reaches the shell.
				x.s.audit.Log(x.entry(opExecCommand, shell.Mask(m.Text)))
				if _, err := stdinW.Write([]byte(m.Text + "\r")); err != nil {
					end("the shell stopped reading")
					break loop
				}
			case "key":
				b, ok := shell.Keys[m.Key]
				if !ok {
					continue
				}
				idle.Reset(cfg.IdleTimeout)
				x.seq++
				x.s.audit.Log(x.entry(opExecKey, "^"+string(rune('A'+b-1))))
				if _, err := stdinW.Write([]byte{b}); err != nil {
					end("the shell stopped reading")
					break loop
				}
			case "resize":
				if m.Cols > 0 && m.Rows > 0 {
					select {
					case x.sizes <- remotecommand.TerminalSize{Width: m.Cols, Height: m.Rows}:
					default: // a burst of resizes: the latest one that fits is enough
					}
				}
			}
		}
	}

	cancel()
	_ = stdinW.Close()
	close(x.sizes)
	select {
	case <-shellDone:
	case <-time.After(5 * time.Second):
		slog.Warn("a shell did not end with its session", "session", x.id, "pod", x.t.Pod)
	}

	reasonMu.Lock()
	why := reason
	reasonMu.Unlock()
	x.s.audit.Log(x.entry(opExecEnd, fmt.Sprintf("%s after %s, %d lines",
		why, time.Since(x.started).Round(time.Second), x.lines)))
	x.status(map[string]any{"type": "closed", "reason": why})
	x.writeMu.Lock()
	_ = x.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	x.writeMu.Unlock()
}

// Compile-time checks: the session is the exec's output and its size queue.
var (
	_ io.Writer                       = (*shellSession)(nil)
	_ remotecommand.TerminalSizeQueue = (*shellSession)(nil)
)
