package kube

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
)

// HelmRelease is a release as the UI needs it.
type HelmRelease struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Revision  int    `json:"revision"`
	Status    string `json:"status"`
	Chart     string `json:"chart"`
	Version   string `json:"version"`
	Updated   string `json:"updated"`
}

// helmLog keeps what the SDK narrates while one action runs.
//
// Which resource could not be deleted is said only through this callback, and
// then the SDK returns a bare "failed to delete release". Letting the lines go
// costs the only thing worth knowing: the trail says "failed", and a fortnight
// later nobody can tell a missing permission from a webhook or from a version
// the cluster no longer serves. That is not a hypothetical — it is how a
// half-removed release sat unnoticed for eleven days.
type helmLog struct {
	mu    sync.Mutex
	lines []string
}

// keptHelmLines bounds what is held. The line that matters is the last one, so
// it is the oldest that gets dropped.
const keptHelmLines = 20

func (l *helmLog) record(format string, v ...any) {
	msg := fmt.Sprintf(format, v...)
	slog.Debug("helm: " + msg)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, msg)
	if len(l.lines) > keptHelmLines {
		l.lines = l.lines[len(l.lines)-keptHelmLines:]
	}
}

// trouble returns the recorded lines that look like something going wrong.
//
// Every ordinary step is narrated through the same callback, and all of it in
// an error message buries the one line worth reading. Matching on words is
// crude; the alternative is to show the whole narration or none of it.
func (l *helmLog) trouble() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	var out []string
	for _, line := range l.lines {
		low := strings.ToLower(line)
		for _, word := range []string{"failed", "error", "forbidden", "denied", "unable", "could not", "no matches"} {
			if strings.Contains(low, word) {
				out = append(out, strings.TrimSpace(line))
				break
			}
		}
	}
	return strings.Join(out, "; ")
}

// explain puts what the SDK said into an error that does not say it itself.
func explain(err error, detail string) error {
	if err == nil || detail == "" || strings.Contains(err.Error(), detail) {
		return err
	}
	return fmt.Errorf("%w: %s", err, detail)
}

// helmActionConfig prepares the Helm SDK for one namespace, and hands back the
// log the action will narrate into.
func (m *Manager) helmActionConfig(asUser, namespace string) (*action.Configuration, *helmLog, error) {
	getter := newRESTClientGetter(m.restConfig(asUser), namespace)
	actionCfg := new(action.Configuration)
	logs := &helmLog{}
	// The "secret" driver keeps releases in namespace secrets — the Helm 3 default.
	if err := actionCfg.Init(getter, namespace, "secret", logs.record); err != nil {
		return nil, nil, fmt.Errorf("helm init: %w", err)
	}
	return actionCfg, logs, nil
}

// ListReleases returns the Helm releases in a namespace.
func (m *Manager) ListReleases(asUser, namespace string) ([]HelmRelease, error) {
	actionCfg, _, err := m.helmActionConfig(asUser, namespace)
	if err != nil {
		return nil, err
	}
	list := action.NewList(actionCfg)
	list.All = true
	rels, err := list.Run()
	if err != nil {
		return nil, err
	}
	out := make([]HelmRelease, 0, len(rels))
	for _, r := range rels {
		out = append(out, toHelmRelease(r))
	}
	return out, nil
}

// ReleaseHistory returns a release's revision history.
func (m *Manager) ReleaseHistory(asUser, namespace, name string) ([]HelmRelease, error) {
	actionCfg, _, err := m.helmActionConfig(asUser, namespace)
	if err != nil {
		return nil, err
	}
	hist := action.NewHistory(actionCfg)
	rels, err := hist.Run(name)
	if err != nil {
		return nil, err
	}
	out := make([]HelmRelease, 0, len(rels))
	for _, r := range rels {
		out = append(out, toHelmRelease(r))
	}
	return out, nil
}

// RollbackRelease rolls a release back; revision 0 means the previous one.
func (m *Manager) RollbackRelease(asUser, namespace, name string, revision int) error {
	actionCfg, logs, err := m.helmActionConfig(asUser, namespace)
	if err != nil {
		return err
	}
	rb := action.NewRollback(actionCfg)
	rb.Version = revision
	// Deliberately not waiting for readiness. Startup probes can legitimately
	// take minutes, and waiting them out turned successful rollbacks into
	// timeouts that looked like failures. The rollback itself applies
	// immediately; the pods coming up are visible on the pods page.
	rb.Wait = false
	if err := rb.Run(name); err != nil {
		err = explain(err, logs.trouble())
		slog.Warn("helm rollback failed", "release", name, "namespace", namespace, "err", err)
		return err
	}
	return nil
}

// UninstallRelease removes a Helm release. When a resource cannot be deleted the
// SDK reports a bare "failed to delete release", and what actually happened
// depends on the SDK version and
// where it failed: the release may be left behind in an uninstalling state (as
// happens when the service account cannot delete a custom resource the chart
// contains), or it may already be gone. So after an error we check what is
// actually true: still there means a real failure, gone means success with a
// warning about the leftovers. Either way the SDK's own account of it is
// carried out, because the returned error does not contain it.
func (m *Manager) UninstallRelease(asUser, namespace, name string) (string, error) {
	actionCfg, logs, err := m.helmActionConfig(asUser, namespace)
	if err != nil {
		return "", err
	}
	un := action.NewUninstall(actionCfg)
	if _, err = un.Run(name); err == nil {
		return "", nil
	}
	// Taken before anything else runs against this configuration: the history
	// call below narrates into the same log.
	detail := logs.trouble()

	// The release is gone, so the operation did what was asked.
	if _, herr := action.NewHistory(actionCfg).Run(name); errors.Is(herr, driver.ErrReleaseNotFound) {
		slog.Warn("helm uninstall: release removed, but some resources could not be deleted",
			"release", name, "namespace", namespace, "err", err, "detail", detail)
		msg := "The release was removed, but some of its resources could not be " +
			"deleted automatically — usually because the service account lacks " +
			"permission for that kind, or the stored manifest uses an apiVersion " +
			"the cluster no longer serves. Check the namespace for leftovers."
		if detail != "" {
			msg += " Helm reported: " + detail
		}
		return msg, nil
	}

	err = explain(err, detail)
	slog.Warn("helm uninstall failed", "release", name, "namespace", namespace, "err", err)
	return "", err
}

func toHelmRelease(r *release.Release) HelmRelease {
	hr := HelmRelease{
		Name:      r.Name,
		Namespace: r.Namespace,
		Revision:  r.Version,
		Status:    r.Info.Status.String(),
	}
	if r.Chart != nil && r.Chart.Metadata != nil {
		hr.Chart = r.Chart.Metadata.Name
		hr.Version = r.Chart.Metadata.Version
	}
	if !r.Info.LastDeployed.IsZero() {
		hr.Updated = r.Info.LastDeployed.UTC().Format("2006-01-02T15:04:05Z")
	}
	return hr
}
