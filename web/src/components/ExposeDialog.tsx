import { useEffect, useMemo, useRef, useState } from "react";
import { useApi } from "../useApi";
import type { IngressForm, IngressRoute, IngressSuggestion } from "../api";
import { checkSummary, SETTING_LABELS, settingNote } from "../ingress";

// Opening a service to the outside: three fields, already filled in from what
// the cluster does — the host its neighbours' pattern gives, the port named
// http, the certificate that covers the host — and a line saying whether it
// will work before anything is created.

type Setting = { key: string; value: string };

export function ExposeDialog({
  namespace,
  service: fixedService,
  services,
  annotationKeys = [],
  onClose,
  onCreated,
}: {
  namespace: string;
  /** The service to expose; when absent, one is picked here. */
  service?: string;
  /** The services that may be picked. */
  services?: string[];
  annotationKeys?: string[];
  onClose: () => void;
  onCreated: (name: string, detail: string) => void;
}) {
  const api = useApi();
  const [service, setService] = useState(fixedService ?? services?.[0] ?? "");
  const [sg, setSg] = useState<IngressSuggestion | null>(null);
  const [name, setName] = useState("");
  const [host, setHost] = useState("");
  const [path, setPath] = useState("/");
  const [port, setPort] = useState("");
  const [tls, setTls] = useState("");
  const [cls, setCls] = useState("");
  const [settings, setSettings] = useState<Setting[]>([]);
  const [more, setMore] = useState(false);
  const [check, setCheck] = useState<IngressRoute[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const timer = useRef<number>();

  // The suggestion for the service, refetched when another one is picked.
  useEffect(() => {
    if (!service) return;
    let live = true;
    setError("");
    api
      .ingressSuggestion(namespace, service)
      .then((s) => {
        if (!live) return;
        setSg(s);
        setName(s.name);
        setHost(s.host);
        setPath(s.path);
        setPort(s.port);
        setTls(s.tls ?? "");
        setCls(s.class);
      })
      .catch((e) => live && setError(e.message));
    return () => {
      live = false;
    };
  }, [api, namespace, service]);

  const form: IngressForm = useMemo(
    () => ({
      class: cls,
      rules: [{ host: host.trim(), path: path.trim() || "/", service, port }],
      tls: tls && host.trim() ? [{ secret: tls, hosts: [host.trim()] }] : [],
      annotations: Object.fromEntries(settings.filter((s) => s.key.trim()).map((s) => [s.key.trim(), s.value])),
    }),
    [cls, host, path, service, port, tls, settings]
  );

  // Checked as it is typed, a moment after typing stops.
  useEffect(() => {
    if (!service || !port || !name) return;
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => {
      api
        .checkIngress(namespace, name, form)
        .then((r) => setCheck(r.routes))
        .catch(() => setCheck(null));
    }, 400);
    return () => window.clearTimeout(timer.current);
  }, [api, namespace, name, form, service, port]);

  const summary = check ? checkSummary(check) : null;

  const create = async () => {
    setBusy(true);
    setError("");
    try {
      const r = await api.createIngress(namespace, name.trim(), form);
      onCreated(name.trim(), r.detail);
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-overlay">
      <div className="modal ing-expose" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <div>
            <div className="muted">{namespace}</div>
            <div className="modal-title">{fixedService ? `Expose ${fixedService}` : "New ingress"}</div>
          </div>
          <button className="icon-btn" onClick={onClose} disabled={busy} aria-label="Close">
            ✕
          </button>
        </div>
        <div className="modal-body pad">
          {error && <div className="alert">{error}</div>}

          {!fixedService && (
            <div className="field">
              <label className="muted">Service</label>
              <select className="input" value={service} onChange={(e) => setService(e.target.value)}>
                {(services ?? []).map((s) => (
                  <option key={s} value={s}>
                    {s}
                  </option>
                ))}
              </select>
            </div>
          )}

          <div className="field">
            <label className="muted">Address</label>
            <div className="ing-addr">
              <input
                className="input mono"
                value={host}
                placeholder="host.example.com"
                onChange={(e) => setHost(e.target.value)}
                spellCheck={false}
              />
              <input
                className="input mono ing-path"
                value={path}
                onChange={(e) => setPath(e.target.value)}
                spellCheck={false}
                aria-label="Path"
              />
            </div>
            {sg?.host && host === sg.host && <div className="field-hint">Like the other hosts around it</div>}
            {sg && !sg.host && <div className="field-hint">No neighbour to take a pattern from: write the host</div>}
          </div>

          <div className="ing-two">
            <div className="field">
              <label className="muted">Port</label>
              <select className="input mono" value={port} onChange={(e) => setPort(e.target.value)}>
                {(sg?.ports ?? []).map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </select>
            </div>
            <div className="field">
              <label className="muted">HTTPS</label>
              <select className="input" value={tls} onChange={(e) => setTls(e.target.value)}>
                <option value="">none — plain HTTP</option>
                {(sg?.secrets ?? []).map((s) => (
                  <option key={s} value={s}>
                    {s}
                    {s === sg?.tls ? " — covers the host" : ""}
                  </option>
                ))}
              </select>
            </div>
          </div>

          {summary && (
            <div className={"ing-check " + (summary.ok ? "ok" : "warn")}>
              {summary.ok ? "✓ " : "⚠ "}
              {summary.text}
            </div>
          )}

          <button className="link-btn ing-more" onClick={() => setMore((v) => !v)}>
            {more ? "▾" : "▸"} More: name, class, settings
          </button>
          {more && (
            <div className="ing-more-body">
              <div className="ing-two">
                <div className="field">
                  <label className="muted">Name</label>
                  <input className="input mono" value={name} onChange={(e) => setName(e.target.value)} />
                </div>
                <div className="field">
                  <label className="muted">Class</label>
                  <input className="input mono" value={cls} onChange={(e) => setCls(e.target.value)} />
                </div>
              </div>
              <SettingsEditor settings={settings} onChange={setSettings} keys={annotationKeys} />
            </div>
          )}
        </div>
        <div className="modal-actions pad">
          <button className="btn ghost" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="btn" onClick={create} disabled={busy || !name.trim() || !service || !port}>
            {busy ? "Creating…" : "Create"}
          </button>
        </div>
      </div>
    </div>
  );
}

/** The settings of an ingress: readable names for the usual annotations, any
    other one as a key and a value. */
export function SettingsEditor({
  settings,
  onChange,
  keys,
  readOnly,
}: {
  settings: Setting[];
  onChange: (s: Setting[]) => void;
  keys: string[];
  readOnly?: boolean;
}) {
  const set = (i: number, patch: Partial<Setting>) =>
    onChange(settings.map((s, j) => (j === i ? { ...s, ...patch } : s)));
  const suggestions = Array.from(new Set([...Object.keys(SETTING_LABELS), ...keys])).sort();
  return (
    <div className="ing-settings">
      <label className="muted">Settings</label>
      {settings.length === 0 && <div className="field-hint">No settings: the controller's defaults apply.</div>}
      {settings.map((s, i) => {
        const known = SETTING_LABELS[s.key];
        const note = settingNote(s.key);
        return (
          <div className="ing-setting" key={i}>
            <div className="ing-setting-key">
              {known && <div className="ing-setting-label">{known.label}</div>}
              <input
                className="input sm mono"
                list="ing-setting-keys"
                value={s.key}
                placeholder="annotation"
                disabled={readOnly}
                onChange={(e) => set(i, { key: e.target.value })}
              />
              {note && <div className="field-hint warn-soft">{note}</div>}
            </div>
            <input
              className="input sm mono"
              value={s.value}
              placeholder={known?.hint ?? "value"}
              disabled={readOnly}
              onChange={(e) => set(i, { value: e.target.value })}
            />
            {!readOnly && (
              <button
                className="icon-btn"
                aria-label="Remove setting"
                onClick={() => onChange(settings.filter((_, j) => j !== i))}
              >
                ✕
              </button>
            )}
          </div>
        );
      })}
      {!readOnly && (
        <button className="link-btn" onClick={() => onChange([...settings, { key: "", value: "" }])}>
          + Add setting
        </button>
      )}
      <datalist id="ing-setting-keys">
        {suggestions.map((k) => (
          <option key={k} value={k}>
            {SETTING_LABELS[k]?.label ?? ""}
          </option>
        ))}
      </datalist>
    </div>
  );
}

export type { Setting };
