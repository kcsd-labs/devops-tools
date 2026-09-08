package kube

import (
	"errors"
	"fmt"
	"log/slog"

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

// helmActionConfig prepares the Helm SDK for one namespace.
func (m *Manager) helmActionConfig(asUser, namespace string) (*action.Configuration, error) {
	getter := newRESTClientGetter(m.restConfig(asUser), namespace)
	actionCfg := new(action.Configuration)
	// The "secret" driver keeps releases in namespace secrets — the Helm 3 default.
	if err := actionCfg.Init(getter, namespace, "secret", func(format string, v ...interface{}) {
		slog.Debug("helm: " + fmt.Sprintf(format, v...)) // operation detail, for diagnosis
	}); err != nil {
		return nil, fmt.Errorf("helm init: %w", err)
	}
	return actionCfg, nil
}

// ListReleases returns the Helm releases in a namespace.
func (m *Manager) ListReleases(asUser, namespace string) ([]HelmRelease, error) {
	actionCfg, err := m.helmActionConfig(asUser, namespace)
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
	actionCfg, err := m.helmActionConfig(asUser, namespace)
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
	actionCfg, err := m.helmActionConfig(asUser, namespace)
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
	return rb.Run(name)
}

// UninstallRelease removes a Helm release. When a resource cannot be deleted the
// SDK reports a bare "failed to delete release", and what actually happened
// depends on the SDK version and
// where it failed: the release may be left behind in an uninstalling state (as
// happens when the service account cannot delete a custom resource the chart
// contains), or it may already be gone. So after an error we check what is
// actually true: still there means a real failure, gone means success with a
// warning about the leftovers.
func (m *Manager) UninstallRelease(asUser, namespace, name string) (string, error) {
	actionCfg, err := m.helmActionConfig(asUser, namespace)
	if err != nil {
		return "", err
	}
	un := action.NewUninstall(actionCfg)
	if _, err = un.Run(name); err == nil {
		return "", nil
	}
	// The release is gone, so the operation did what was asked.
	if _, herr := action.NewHistory(actionCfg).Run(name); errors.Is(herr, driver.ErrReleaseNotFound) {
		slog.Warn("helm uninstall: release removed, but some resources could not be deleted",
			"release", name, "namespace", namespace, "err", err)
		return "The release was removed, but some of its resources could not be " +
			"deleted automatically — usually because the service account lacks " +
			"permission for that kind, or the stored manifest uses an apiVersion " +
			"the cluster no longer serves. Check the namespace for leftovers.", nil
	}
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
