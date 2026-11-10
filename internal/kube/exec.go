package kube

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// ErrNoShell means the container has neither bash nor sh: a distroless image,
// or one built from scratch. Nothing is wrong with it; there is simply nothing
// to open.
var ErrNoShell = errors.New("this container has no shell: its image carries neither bash nor sh")

// ExecTarget is a container a shell may be opened in.
type ExecTarget struct {
	Container string
}

// CheckExecTarget resolves the container and refuses pods a shell must not be
// opened in.
//
// A privileged pod, or one sharing the node's processes, network or
// filesystem, is the node itself to whoever has a shell in it. The permission
// to open shells in a namespace was not meant to hand out its nodes.
func (m *Manager) CheckExecTarget(ctx context.Context, asUser, namespace, pod, container string) (ExecTarget, error) {
	cs, err := m.Clientset(asUser)
	if err != nil {
		return ExecTarget{}, err
	}
	p, err := cs.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return ExecTarget{}, err
	}
	if p.Status.Phase != corev1.PodRunning {
		return ExecTarget{}, fmt.Errorf("the pod is %s, not running", strings.ToLower(string(p.Status.Phase)))
	}
	if reason := nodeAccess(&p.Spec); reason != "" {
		return ExecTarget{}, fmt.Errorf("a shell is not opened in this pod: %s", reason)
	}
	if container == "" {
		container = p.Spec.Containers[0].Name
		if def := p.Annotations["kubectl.kubernetes.io/default-container"]; def != "" {
			container = def
		}
	}
	for _, c := range p.Spec.Containers {
		if c.Name == container {
			return ExecTarget{Container: container}, nil
		}
	}
	return ExecTarget{}, fmt.Errorf("the pod has no container %q", container)
}

// nodeAccess says how a pod reaches past its container, or "" if it does not.
func nodeAccess(spec *corev1.PodSpec) string {
	switch {
	case spec.HostPID:
		return "it shares the node's processes (hostPID)"
	case spec.HostNetwork:
		return "it shares the node's network (hostNetwork)"
	case spec.HostIPC:
		return "it shares the node's IPC (hostIPC)"
	}
	for _, v := range spec.Volumes {
		if v.HostPath != nil {
			return "it mounts a directory of the node (hostPath " + v.HostPath.Path + ")"
		}
	}
	for _, c := range spec.Containers {
		if sc := c.SecurityContext; sc != nil {
			if sc.Privileged != nil && *sc.Privileged {
				return "container " + c.Name + " is privileged"
			}
			if sc.Capabilities != nil {
				for _, add := range sc.Capabilities.Add {
					if add == "SYS_ADMIN" || add == "ALL" {
						return "container " + c.Name + " has " + string(add)
					}
				}
			}
		}
	}
	return ""
}

// FindShell returns the shell to open: bash where the image has it, sh
// otherwise. Asked of the container itself, with a command that needs only sh.
func (m *Manager) FindShell(ctx context.Context, asUser, namespace, pod, container string) (string, error) {
	var out, errOut bytes.Buffer
	err := m.stream(ctx, asUser, namespace, pod, container,
		[]string{"sh", "-c", "command -v bash || command -v sh"}, false, nil, &out, &errOut, nil)
	path := strings.TrimSpace(out.String())
	if err != nil || path == "" {
		msg := err
		if msg == nil {
			msg = errors.New(strings.TrimSpace(errOut.String()))
		}
		if strings.Contains(msg.Error(), "not found") || strings.Contains(msg.Error(), "no such file") {
			return "", ErrNoShell
		}
		return "", fmt.Errorf("could not look for a shell: %w", msg)
	}
	if i := strings.IndexByte(path, '\n'); i >= 0 {
		path = path[:i]
	}
	return path, nil
}

// OpenShell runs an interactive shell with a terminal, until it exits or ctx
// ends. Input comes from stdin, the terminal's output goes to stdout, and
// sizes carries the window size as it changes.
func (m *Manager) OpenShell(ctx context.Context, asUser, namespace, pod, container, shellPath string,
	stdin io.Reader, stdout io.Writer, sizes remotecommand.TerminalSizeQueue) error {
	cmd := []string{shellPath}
	if strings.HasSuffix(shellPath, "/bash") {
		// No history expansion: with it, "!!" or "!-2" would run something
		// other than the line on record.
		cmd = append(cmd, "+H")
	}
	return m.stream(ctx, asUser, namespace, pod, container, cmd, true, stdin, stdout, nil, sizes)
}

// stream runs one exec, over WebSocket where the API server offers it and
// SPDY where it does not.
func (m *Manager) stream(ctx context.Context, asUser, namespace, pod, container string, cmd []string, tty bool,
	stdin io.Reader, stdout, stderr io.Writer, sizes remotecommand.TerminalSizeQueue) error {
	cfg := m.restConfig(asUser)
	cs, err := m.Clientset(asUser)
	if err != nil {
		return err
	}
	req := cs.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(namespace).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   cmd,
			Stdin:     stdin != nil,
			Stdout:    true,
			Stderr:    !tty && stderr != nil,
			TTY:       tty,
		}, scheme.ParameterCodec)

	ws, err := remotecommand.NewWebSocketExecutor(cfg, "GET", req.URL().String())
	if err != nil {
		return err
	}
	spdy, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
	if err != nil {
		return err
	}
	exec, err := remotecommand.NewFallbackExecutor(ws, spdy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return err
	}
	opts := remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Tty: tty, TerminalSizeQueue: sizes}
	if !tty {
		opts.Stderr = stderr
	}
	return exec.StreamWithContext(ctx, opts)
}
