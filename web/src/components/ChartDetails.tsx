import { useEffect, useState } from "react";
import { useApi } from "../useApi";
import type { CatalogChart, CatalogPackage } from "../api";
import { Markdown } from "./Markdown";

// What a chart is, before anyone installs it: its description, versions, where
// it is published, and its README.

export function ChartDetails({
  namespace,
  pkg,
  onClose,
  onInstall,
}: {
  namespace: string;
  pkg: CatalogPackage;
  onClose: () => void;
  onInstall: () => void;
}) {
  const api = useApi();
  const [info, setInfo] = useState<CatalogChart | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    let live = true;
    api
      .catalogChart(namespace, pkg.repo, pkg.name)
      .then((c) => live && setInfo(c))
      .catch((e) => live && setError(e.message));
    return () => {
      live = false;
    };
  }, [api, namespace, pkg.repo, pkg.name]);

  const p = info ?? pkg;
  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal cat-install" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <div className="cat-head">
            <div className="cat-logo-box big">{p.logo ? <img src={p.logo} alt="" /> : <span>⎈</span>}</div>
            <div>
              <div className="modal-title">
                {p.name} <span className="cm-pill">{p.repoTitle}</span>
                {p.official && <span className="tag">official</span>}
                {p.deprecated && <span className="tag warn">deprecated</span>}
              </div>
              <div className="muted">{p.description}</div>
            </div>
          </div>
          <button className="icon-btn" onClick={onClose} aria-label="Close">
            ✕
          </button>
        </div>
        <div className="modal-body pad">
          <div className="cat-meta">
            <div>
              <div className="muted">Latest version</div>
              <b>{p.version}</b>
            </div>
            <div>
              <div className="muted">App version</div>
              <b>{p.appVersion || "—"}</b>
            </div>
            <div>
              <div className="muted">Stars</div>
              <b>★ {p.stars}</b>
            </div>
            <div>
              <div className="muted">Versions</div>
              <b>{info ? info.versions.length : "…"}</b>
            </div>
            <div className="cat-meta-wide">
              <div className="muted">Published at</div>
              <span className="mono">{info?.repoUrl ?? "…"}</span>
            </div>
          </div>
          {error && <div className="alert">{error}</div>}
          {!info && !error && <div className="muted pad">Fetching the chart…</div>}
          {info && (
            <div className="cat-readme-md">
              {info.readme ? <Markdown source={info.readme} /> : <div className="muted">The chart has no README.</div>}
            </div>
          )}
        </div>
        <div className="modal-actions pad">
          {info?.page && (
            <a className="link cat-note" href={info.page} target="_blank" rel="noopener noreferrer">
              Open on Artifact Hub ↗
            </a>
          )}
          <button className="btn ghost" onClick={onClose} style={info?.page ? undefined : { marginLeft: "auto" }}>
            Close
          </button>
          <button className="btn" onClick={onInstall}>
            Install
          </button>
        </div>
      </div>
    </div>
  );
}
