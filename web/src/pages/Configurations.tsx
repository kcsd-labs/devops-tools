import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import CodeMirror from "@uiw/react-codemirror";
import { yaml } from "@codemirror/lang-yaml";
import { indentationMarkers } from "@replit/codemirror-indentation-markers";
import { useApi } from "../useApi";
import { useDialogs } from "../components/Dialogs";
import { SideBySide } from "../components/SideBySide";
import { ApiError } from "../api";
import type { ConfigEntry, ConfigFile } from "../api";

// The service configuration held in Consul.
//
// Only the paths the caller was granted are listed — the filtering happens on
// the server, so the names of systems nobody has access to never reach the
// browser.
export function Configurations() {
  const api = useApi();
  const theme = useEditorTheme();
  const { dialogs, confirm, toast } = useDialogs();

  const [entries, setEntries] = useState<ConfigEntry[]>([]);
  // An empty list means one of two things, and they need different answers.
  const [granted, setGranted] = useState(true);
  const [root, setRoot] = useState("");
  const [kind, setKind] = useState("");
  // Both sides, when a repository and the Consul it is copied into are both
  // configured. `side` is the one being looked at.
  const [sides, setSides] = useState<string[]>([]);
  const [side, setSide] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [search, setSearch] = useState("");
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});

  const [file, setFile] = useState<ConfigFile | null>(null);
  const [draft, setDraft] = useState("");
  const [loadingFile, setLoadingFile] = useState(false);
  const [fileError, setFileError] = useState("");
  const [saving, setSaving] = useState(false);
  // Set when Consul refused the write because the entry moved on. The editor
  // keeps the text — retyping it would be the worst possible outcome — and
  // offers to show what is there now.
  const [conflict, setConflict] = useState(false);

  // The two sides of the open file, when asked for. Fetched on demand rather
  // than with every file: it doubles the reads, and most of the time the badge
  // already answers the question.
  const [compare, setCompare] = useState<{ git: string; consul: string } | null>(null);
  const [comparing, setComparing] = useState(false);

  // As elsewhere: a silently renewed token must not reload the list.
  const apiRef = useRef(api);
  apiRef.current = api;

  // Files already read, and reads already under way.
  //
  // Between pointing at an entry and clicking it there is a fifth of a second
  // doing nothing, which is most of what the round trip costs. Starting the
  // read on hover spends that time instead of the user's.
  const wanted = useRef("");
  const cache = useRef(new Map<string, { file: ConfigFile; at: number }>());
  const inFlight = useRef(new Map<string, Promise<ConfigFile>>());

  // Deliberately very short.
  //
  // The speed comes from reading on hover, which reads afresh and merely starts
  // earlier. This only covers flicking between the two sides and back, or a
  // double click — and beyond a few seconds it stops being the same glance and
  // starts being old news. What is on screen carries a drift badge worked out
  // when it was read, and a badge saying "in sync" about a file somebody
  // changed half a minute ago is worse than a moment's wait.
  //
  // Saving is safe regardless: the version travels with the content, and a
  // stale one is refused rather than silently applied.
  const CACHE_MS = 5_000;

  const read = useCallback((path: string, from: string): Promise<ConfigFile> => {
    const key = `${from}:${path}`;
    const running = inFlight.current.get(key);
    if (running) return running;
    const p = apiRef.current
      .config(path, from || undefined)
      .then((f) => {
        cache.current.set(key, { file: f, at: Date.now() });
        return f;
      })
      .finally(() => inFlight.current.delete(key));
    inFlight.current.set(key, p);
    return p;
  }, []);

  const cached = (path: string, from: string): ConfigFile | null => {
    const hit = cache.current.get(`${from}:${path}`);
    return hit && Date.now() - hit.at < CACHE_MS ? hit.file : null;
  };

  const prefetch = (path: string) => {
    if (cached(path, side)) return;
    void read(path, side).catch(() => {
      // A failure here is not worth reporting: nobody asked for this file yet.
      // Clicking it will ask properly, and report properly.
    });
  };

  const load = useCallback((want?: string) => {
    apiRef.current
      .configs(want)
      .then((r) => {
        setEntries(r.entries);
        setGranted(r.granted);
        setRoot(r.root);
        setKind(r.kind);
        setSides(r.sides ?? []);
        setSide(r.side ?? "");
        setError("");
      })
      .catch((e) => setError(e.message))
      .finally(() => setLoaded(true));
  }, []);

  useEffect(() => load(), [load]);

  // Re-read the open file when this window comes back to the front.
  //
  // Opening a file shows what was there at that moment, and then the screen
  // stops moving — which is fine for an editor, but here the file carries a
  // badge saying whether the two sides agree, and that is a claim about now.
  // Going off to GitLab or a terminal and coming back is exactly when it may
  // have stopped being true.
  // Read through refs so the listener is attached once per file rather than
  // once per keystroke.
  const live = useRef({ file, draft, side, saving });
  live.current = { file, draft, side, saving };

  useEffect(() => {
    if (!file) return;
    const onFocus = async () => {
      const { file, draft, side, saving } = live.current;
      if (!file || saving) return;
      // Just read — alt-tabbing twice in a second is not two questions.
      if (cached(file.path, side)) return;

      let fresh: ConfigFile;
      try {
        fresh = await read(file.path, side);
      } catch {
        return; // a failed check is not news; opening it again will report properly
      }
      if (wanted.current !== file.path) return;
      if (fresh.content === file.content && fresh.drift === file.drift) return;

      if (draft !== file.content) {
        // Edits in progress. Saying so beats replacing what somebody typed —
        // and the save would be refused anyway, which is a worse way to find out.
        setFileError(
          `${file.path} changed since you opened it. Your text is untouched; saving it will be ` +
            `refused until you compare with what is there now.`
        );
        return;
      }
      setFile(fresh);
      setDraft(fresh.content);
      toast(`${file.path} was updated elsewhere — reloaded`);
    };
    window.addEventListener("focus", onFocus);
    return () => window.removeEventListener("focus", onFocus);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [file?.path]);

  const dirty = file !== null && draft !== file.content;

  const open = async (path: string) => {
    if (path === file?.path) return;
    if (dirty) {
      const ok = await confirm({
        title: "Discard your changes?",
        message: `${file?.path} has edits that have not been saved.`,
        okText: "Discard",
        danger: true,
      });
      if (!ok) return;
    }
    setFileError("");
    setConflict(false);
    setCompare(null);

    // Already read a moment ago — show it at once. The read below still runs,
    // so what is on screen becomes current without anybody waiting for it.
    const hit = cached(path, side);
    if (hit) {
      setFile(hit);
      setDraft(hit.content);
    } else {
      setLoadingFile(true);
    }

    wanted.current = path;
    try {
      const f = await read(path, side);
      // Two clicks in quick succession finish in whatever order the network
      // decides, so the answer to an abandoned one is dropped rather than
      // allowed to replace what is now on screen.
      if (wanted.current !== path) return;
      setFile(f);
      // The text is only replaced if nothing has been typed since it appeared.
      // Overwriting somebody's edit is worse than leaving it slightly behind —
      // and a save against the older version is refused, not silently applied.
      setDraft((cur) => (hit && cur !== hit.content ? cur : f.content));
    } catch (e: any) {
      if (wanted.current !== path) return;
      if (!hit) setFile(null);
      setFileError(e.message);
    } finally {
      setLoadingFile(false);
    }
  };

  // Switching sides keeps the file open and shows the other side of it.
  //
  // Looking at one side and then the other is how the two get compared by eye,
  // and having to find the same path again in the tree is a step that exists
  // for no reason. Unsaved edits are still confirmed away: the text belongs to
  // the side it was typed on.
  const switchSide = async (want: string) => {
    if (want === side) return;
    if (dirty) {
      const ok = await confirm({
        title: "Discard your changes?",
        message: `${file?.path} has edits that have not been saved.`,
        okText: "Discard",
        danger: true,
      });
      if (!ok) return;
    }
    const keep = file?.path ?? "";
    setDraft("");
    setFileError("");
    setConflict(false);
    setCompare(null);
    setLoaded(false);
    setSide(want);
    load(want);

    if (!keep) {
      setFile(null);
      return;
    }
    setLoadingFile(true);
    wanted.current = keep; // same guard as open(): a stale answer must not land
    try {
      const f = await read(keep, want);
      if (wanted.current !== keep) return;
      setFile(f);
      setDraft(f.content);
    } catch (e: any) {
      // Usually because that side does not have this file — which is a finding
      // rather than a fault, and the path is kept on screen so it can be read.
      setFile(null);
      setFileError(`${keep}: ${e.message}`);
    } finally {
      setLoadingFile(false);
    }
  };

  const save = async () => {
    if (!file) return;
    setSaving(true);
    setFileError("");
    try {
      const res = await api.saveConfig(file.path, draft, file.version);
      // A warning means it committed but the copy is behind — worth saying
      // plainly, since until it catches up the services still run the old
      // values, and that is the difference between saved and in effect.
      cache.current.clear();
      if (res.warning) setFileError(res.warning);
      else toast(`${file.path} saved`);
      setConflict(false);
      await reopen(file.path);
      load(side || undefined);
    } catch (e: any) {
      if (e instanceof ApiError && e.status === 409) {
        setConflict(true);
        setFileError(
          "Somebody else changed this entry after you opened it. Your text is still here; " +
            "compare it with what is there now before saving over it."
        );
      } else {
        setFileError(e.message);
      }
    } finally {
      setSaving(false);
    }
  };

  // The emergency path: write Consul and leave the repository behind, when a
  // service has to be fixed now and a pipeline is too slow. It deliberately
  // creates a difference, so it says so before doing it and the page shows the
  // result afterwards.
  const saveConsulOnly = async () => {
    if (!file) return;
    const ok = await confirm({
      title: "Write Consul only, without committing?",
      message:
        "The services pick this up at once, but the repository will not have it — and the " +
        "synchroniser will overwrite it on its next pass, taking your change with it. " +
        "Use this to get a service running, then make the same change properly.",
      okText: "Write Consul only",
      danger: true,
    });
    if (!ok) return;
    setSaving(true);
    setFileError("");
    try {
      await api.saveConfigSide(file.path, draft, "consul");
      cache.current.clear();
      toast(`${file.path} written to Consul only`);
      await reopen(file.path);
    } catch (e: any) {
      setFileError(e.message);
    } finally {
      setSaving(false);
    }
  };

  // Re-read rather than guess the new state: the next save needs the version
  // the source actually assigned, and the difference between the sides has just
  // changed.
  const reopen = async (path: string) => {
    cache.current.clear();
    const f = await read(path, side);
    setFile(f);
    setDraft(f.content);
    // Whatever was on screen described the old state of both sides.
    setCompare(null);
  };

  // Read both sides and show them together. "They differ" is only half an
  // answer; where they differ is the half worth having.
  const showComparison = async () => {
    if (!file) return;
    setComparing(true);
    setFileError("");
    try {
      const [g, c] = await Promise.all([
        api.config(file.path, "git").catch(() => null),
        api.config(file.path, "consul").catch(() => null),
      ]);
      setCompare({ git: g?.content ?? "", consul: c?.content ?? "" });
    } catch (e: any) {
      setFileError(e.message);
    } finally {
      setComparing(false);
    }
  };

  // Reload after a conflict: replaces the editor with what is in Consul now.
  const reload = async () => {
    if (!file) return;
    const ok = await confirm({
      title: "Replace your text with the current entry?",
      message: "Your unsaved edits are lost. Copy anything you need first.",
      okText: "Replace",
      danger: true,
    });
    if (!ok) return;
    await reopen(file.path);
    setConflict(false);
    setFileError("");
  };

  const q = search.trim().toLowerCase();
  const shown = q ? entries.filter((e) => e.path.toLowerCase().includes(q)) : entries;
  const tree = useMemo(() => buildTree(shown), [shown]);

  return (
    <div className="page">
      {dialogs}

      <div className="page-head">
        <div className="row head-left">
          <h1 className="page-title">Configurations</h1>
          {/* Where a save actually lands. Writing to Consul while a
              synchroniser copies the repository over it loses the change on the
              next run, so which one this is must never be a guess. */}
          {/* With both configured this is a switch, not a label: which side you
              are looking at is a thing you change, and which side a save
              reaches follows from it. */}
          {sides.length > 1 ? (
            <div className="seg" title={root}>
              {sides.map((sname) => (
                <button
                  key={sname}
                  className={"seg-btn" + (sname === side ? " on" : "")}
                  onClick={() => switchSide(sname)}
                >
                  {SIDE_LABELS[sname] ?? sname}
                </button>
              ))}
            </div>
          ) : (
            kind && (
              <span className="role-chip" title={root}>
                {kind === "git" ? "Git" : "Consul"}
              </span>
            )
          )}
        </div>
        {entries.length > 0 && (
          <input
            className="input sm"
            placeholder="Filter by path…"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        )}
      </div>

      {error && <div className="alert">{error}</div>}
      {!loaded && !error && <div className="muted">Loading…</div>}

      {loaded && !error && entries.length === 0 && !granted && (
        <div className="card">
          <div className="card-title">Your roles do not cover any configuration</div>
          <p className="muted">
            Access here is granted by path, under Access management — separately from
            namespaces, and not implied by a role that reaches every namespace.
          </p>
        </div>
      )}

      {loaded && !error && entries.length === 0 && granted && (
        <div className="card">
          <div className="card-title">Nothing under the configured prefix</div>
          <p className="muted">
            Your roles grant access, but nothing came back from{" "}
            <code>{root || "the configured source"}</code>.
          </p>
          <p className="muted">
            Two things to check: that the deployment points at where the configuration
            actually lives, and that the paths in your roles are written{" "}
            <em>relative to that</em> — <code>abs</code>, not the whole thing again.
          </p>
        </div>
      )}

      {entries.length > 0 && (
        <div className="cfg-layout">
          {/* Not `no-pad`: that sets overflow:hidden at a specificity this
              cannot outrank, and the tree would be clipped with no scrollbar.
              It carries its own padding instead. */}
          <div className="card cfg-tree">
            {shown.length === 0 ? (
              <div className="empty-row muted">Nothing matches.</div>
            ) : (
              <TreeNodes
                nodes={tree}
                depth={0}
                selected={file?.path ?? ""}
                collapsed={collapsed}
                onToggle={(p) => setCollapsed((c) => ({ ...c, [p]: !c[p] }))}
                onOpen={open}
                onHover={prefetch}
              />
            )}
          </div>

          <div className="card cfg-editor">
            {!file && !loadingFile && !fileError && (
              <p className="muted">Choose an entry on the left.</p>
            )}
            {loadingFile && <div className="muted">Loading…</div>}
            {/* A conflict is not a failure — nothing is broken and there is
                something to do about it — so it gets the amber banner that
                carries an action rather than the red one. */}
            {fileError && (
              <div className={conflict ? "alert alert-action" : "alert"}>{fileError}</div>
            )}

            {file && (
              <>
                <div className="cfg-head">
                  <div>
                    <div className="cfg-path">
                      {file.path}
                      {file.drift && (
                        <span className={"cfg-drift " + DRIFT[file.drift].tone}>
                          {DRIFT[file.drift].label}
                        </span>
                      )}
                    </div>
                    <div className="muted cfg-sub">
                      {file.writable ? "You may edit this entry" : "Read-only for your roles"}
                      {/* Only Git knows who last touched a file — Consul keeps
                          no author, and inventing one would be worse than the
                          gap. */}
                      {file.author && ` · last changed by ${file.author}`}
                      {file.updatedAt && ` on ${new Date(file.updatedAt).toLocaleString()}`}
                      {dirty && " · unsaved changes"}
                    </div>
                  </div>
                  <div className="row">
                    {/* Only where there are two sides to put next to each
                        other. Offered whatever the state: "in sync" is worth
                        being able to confirm, not just be told. */}
                    {sides.length > 1 &&
                      (compare ? (
                        <button className="btn ghost" onClick={() => setCompare(null)}>
                          Back to editor
                        </button>
                      ) : (
                        <button className="btn ghost" disabled={comparing} onClick={showComparison}>
                          {comparing ? "Comparing…" : "Compare sides"}
                        </button>
                      ))}
                    {conflict && (
                      <button className="btn ghost" onClick={reload}>
                        Show current
                      </button>
                    )}
                    {/* Only where there is a repository to bypass, and only on
                        its side: from the Consul view an ordinary save already
                        is a Consul write. */}
                    {file.writable && sides.length > 1 && side === "git" && (
                      <button
                        className="btn ghost"
                        disabled={!dirty || saving}
                        onClick={saveConsulOnly}
                        title="Get a service running now; the repository will not have it"
                      >
                        Consul only
                      </button>
                    )}
                    {file.writable && (
                      <button className="btn" disabled={!dirty || saving} onClick={save}>
                        {saving ? "Saving…" : sides.length > 1 ? "Commit and apply" : "Save"}
                      </button>
                    )}
                  </div>
                </div>

                {compare ? (
                  <SideBySide
                    leftLabel="Git"
                    rightLabel="Consul"
                    left={compare.git}
                    right={compare.consul}
                  />
                ) : (
                  <CodeMirror
                    value={draft}
                    theme={theme}
                    height="60vh"
                    extensions={[yaml(), indentationMarkers()]}
                    editable={file.writable}
                    onChange={setDraft}
                  />
                )}
              </>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

const SIDE_LABELS: Record<string, string> = { git: "Git", consul: "Consul" };

// How the two sides stand. Which one is newer is never claimed: Consul records
// no time at all, so an ordering would be invented — and acting on an invented
// one means overwriting the wrong side.
const DRIFT: Record<string, { label: string; tone: string }> = {
  match: { label: "in sync with Consul", tone: "ok" },
  differ: { label: "differs from Consul", tone: "warn" },
  "missing-in-consul": { label: "not in Consul yet", tone: "warn" },
  "missing-in-git": { label: "not in the repository", tone: "warn" },
};

// --- the tree --------------------------------------------------------------

type Node = {
  name: string;
  path: string;
  children: Node[];
  entry?: ConfigEntry;
};

// buildTree turns the flat list of keys into folders. Consul has no folders —
// the slashes in a key are the only structure there is — but a flat list of a
// few hundred keys is unreadable.
function buildTree(entries: ConfigEntry[]): Node[] {
  const roots: Node[] = [];
  for (const e of entries) {
    const parts = e.path.split("/");
    let level = roots;
    let prefix = "";
    parts.forEach((part, i) => {
      prefix = prefix ? `${prefix}/${part}` : part;
      const leaf = i === parts.length - 1;
      let node = level.find((n) => n.name === part && !!n.entry === leaf);
      if (!node) {
        node = { name: part, path: prefix, children: [] };
        level.push(node);
      }
      if (leaf) node.entry = e;
      level = node.children;
    });
  }
  return sortNodes(roots);
}

// Folders before entries, each alphabetically — the order a file browser uses,
// and the one people scan for.
function sortNodes(nodes: Node[]): Node[] {
  nodes.sort((a, b) => {
    const af = a.entry ? 1 : 0;
    const bf = b.entry ? 1 : 0;
    if (af !== bf) return af - bf;
    return a.name.localeCompare(b.name);
  });
  for (const n of nodes) sortNodes(n.children);
  return nodes;
}

function TreeNodes({
  nodes,
  depth,
  selected,
  collapsed,
  onToggle,
  onOpen,
  onHover,
}: {
  nodes: Node[];
  depth: number;
  selected: string;
  collapsed: Record<string, boolean>;
  onToggle: (path: string) => void;
  onOpen: (path: string) => void;
  onHover: (path: string) => void;
}) {
  return (
    <ul className="cfg-list">
      {nodes.map((n) => {
        const isFolder = !n.entry;
        const shut = collapsed[n.path];
        return (
          <li key={n.path + (isFolder ? "/" : "")}>
            <button
              className={
                "cfg-item" +
                (isFolder ? " folder" : "") +
                (n.path === selected ? " active" : "") +
                (n.entry && !n.entry.writable ? " ro" : "")
              }
              style={{ paddingLeft: 10 + depth * 14 }}
              onClick={() => (isFolder ? onToggle(n.path) : onOpen(n.path))}
              onMouseEnter={() => (isFolder ? undefined : onHover(n.path))}
              onFocus={() => (isFolder ? undefined : onHover(n.path))}
            >
              <span className="cfg-caret">{isFolder ? (shut ? "▸" : "▾") : ""}</span>
              <span className="cfg-name">{n.name}</span>
              {n.entry && !n.entry.writable && (
                <span className="cfg-badge" title="Read-only for your roles">
                  view
                </span>
              )}
            </button>
            {isFolder && !shut && (
              <TreeNodes
                nodes={n.children}
                depth={depth + 1}
                selected={selected}
                collapsed={collapsed}
                onToggle={onToggle}
                onOpen={onOpen}
                onHover={onHover}
              />
            )}
          </li>
        );
      })}
    </ul>
  );
}

// --- theme -----------------------------------------------------------------

// The editor has to follow the portal's theme, which lives on the root element
// and is toggled from the top bar. Watching the attribute is what keeps the two
// in step without threading the theme through every page.
function useEditorTheme(): "light" | "dark" {
  const read = () => (document.documentElement.dataset.theme === "light" ? "light" : "dark");
  const [theme, setTheme] = useState<"light" | "dark">(read);
  useEffect(() => {
    const obs = new MutationObserver(() => setTheme(read()));
    obs.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    return () => obs.disconnect();
  }, []);
  return theme;
}
