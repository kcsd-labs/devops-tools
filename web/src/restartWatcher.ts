// Background watcher for rolling restarts.
//
// After "Restart" is pressed the interesting part happens over the next few
// minutes: the new pod is created, pulls its image and passes its probes. This
// module follows that from outside React, so the progress survives navigation
// and a full page reload (state is mirrored into localStorage), and reports the
// outcome with a toast wherever the user happens to be.
//
// It also diagnoses a stuck rollout — no image, crash loop, no room to
// schedule — so the UI can explain the situation instead of spinning forever.

import { pushToast } from "./toasts";
import { authHeaders } from "./session";

export type RestartState = {
  ns: string;
  kind: string; // Deployment | StatefulSet | DaemonSet
  name: string;
  phase: "progressing" | "stuck" | "done" | "failed";
  reason: string; // image | crash | config | unschedulable | ""
  reasonPod: string;
  ready: number;
  desired: number;
};

type Watcher = RestartState & { timer?: number; fails: number; warned: boolean; startedAt: number };

const watchers = new Map<string, Watcher>();
const subs = new Set<() => void>();

const POLL_MS = 4000;
const MAX_FAILS = 15; // ~1 minute of consecutive errors before giving up
const CLIENT_DEADLINE_MS = 15 * 60_000; // fallback when the workload has no deadline of its own
const STORAGE_KEY = "devops-tools.restartWatchers.v1";

export const restartKey = (ns: string, kind: string, name: string) => `${ns}/${kind}/${name}`;

export function getRestartStates(ns?: string): RestartState[] {
  const out: RestartState[] = [];
  watchers.forEach((w) => {
    if (!ns || w.ns === ns) {
      const { timer, fails, warned, startedAt, ...state } = w;
      void timer;
      void fails;
      void warned;
      void startedAt;
      out.push(state);
    }
  });
  return out;
}

export function subscribeRestarts(fn: () => void): () => void {
  subs.add(fn);
  return () => {
    subs.delete(fn);
  };
}

function emit() {
  subs.forEach((fn) => fn());
  persist();
}

function persist() {
  try {
    const obj: Record<string, { ns: string; kind: string; name: string; startedAt: number }> = {};
    watchers.forEach((w, k) => {
      if (w.phase === "progressing" || w.phase === "stuck") {
        obj[k] = { ns: w.ns, kind: w.kind, name: w.name, startedAt: w.startedAt };
      }
    });
    localStorage.setItem(STORAGE_KEY, JSON.stringify(obj));
  } catch {
    /* storage full or disabled — the watcher still works for this page */
  }
}

export function startRestartWatch(ns: string, kind: string, name: string) {
  if (!kind || !name) return;
  const key = restartKey(ns, kind, name);
  const existing = watchers.get(key);
  if (existing && (existing.phase === "progressing" || existing.phase === "stuck")) return;
  if (existing?.timer) window.clearTimeout(existing.timer);

  watchers.set(key, {
    ns,
    kind,
    name,
    phase: "progressing",
    reason: "",
    reasonPod: "",
    ready: 0,
    desired: 0,
    fails: 0,
    warned: false,
    startedAt: Date.now(),
  });
  emit();
  schedule(key, 1500); // first check quickly, then settle into the poll interval
}

export function clearRestartWatch(key: string) {
  const w = watchers.get(key);
  if (w?.timer) window.clearTimeout(w.timer);
  watchers.delete(key);
  emit();
}

function schedule(key: string, ms = POLL_MS) {
  const w = watchers.get(key);
  if (!w) return;
  w.timer = window.setTimeout(() => void poll(key), ms);
}

async function poll(key: string) {
  const w = watchers.get(key);
  if (!w) return;
  try {
    const url =
      `/api/namespaces/${w.ns}/rollout-status` +
      `?kind=${encodeURIComponent(w.kind)}&name=${encodeURIComponent(w.name)}`;
    const r = await fetch(url, { headers: authHeaders(), cache: "no-store" });

    if (r.status === 404) {
      finishFail(key, `${w.name}: workload no longer exists — stopped watching.`);
      return;
    }
    if (!r.ok) {
      if (registerFail(key)) return;
      schedule(key);
      return;
    }

    w.fails = 0;
    const d = await r.json();
    w.ready = d.ready ?? 0;
    w.desired = d.desired ?? 0;
    w.reason = d.reason ?? "";
    w.reasonPod = d.reasonPod ?? "";

    if (d.done) {
      w.phase = "done";
      emit();
      pushToast(`${w.name} restarted successfully`, "ok");
      window.setTimeout(() => clearRestartWatch(key), 4000); // let the UI show "done"
      return;
    }
    if (d.deadlineExceeded || Date.now() - w.startedAt > CLIENT_DEADLINE_MS) {
      finishFail(key, failText(w));
      return;
    }

    const stuck = w.reason !== "";
    if (stuck && !w.warned) {
      w.warned = true; // warn once; the chip and banner carry it from here
      pushToast(
        `${w.name}: rollout is stuck — ${reasonText(w.reason)}. The old pod keeps serving traffic.`,
        "warn"
      );
    }
    w.phase = stuck ? "stuck" : "progressing";
    emit();
  } catch {
    if (registerFail(key)) return;
  }
  schedule(key);
}

function registerFail(key: string): boolean {
  const w = watchers.get(key);
  if (!w) return true;
  w.fails += 1;
  if (w.fails >= MAX_FAILS) {
    finishFail(key, `${w.name}: lost track of the rollout (no response from the server).`);
    return true;
  }
  return false;
}

// A terminal failure gets a sticky toast; the state stays so the page can keep
// showing the explanation until the user dismisses it.
function finishFail(key: string, text: string) {
  const w = watchers.get(key);
  if (!w) return;
  if (w.timer) window.clearTimeout(w.timer);
  w.phase = "failed";
  emit();
  pushToast(text, "err", true);
}

export function reasonText(reason: string): string {
  switch (reason) {
    case "image":
      return "the new pod cannot pull its image";
    case "crash":
      return "the new pod keeps crashing on startup";
    case "config":
      return "a referenced secret or config map is missing";
    case "unschedulable":
      return "no node has room for the new pod";
    default:
      return "";
  }
}

function failText(w: Watcher): string {
  const reason = reasonText(w.reason);
  return `${w.name}: rollout did not finish${reason ? " — " + reason : ""}. The old pod keeps serving traffic.`;
}

// Resume watching after a full page reload.
(function rehydrate() {
  let stored: Record<string, { ns?: string; kind?: string; name?: string; startedAt?: number }> = {};
  try {
    stored = JSON.parse(localStorage.getItem(STORAGE_KEY) || "{}") || {};
  } catch {
    return;
  }
  for (const key of Object.keys(stored)) {
    const p = stored[key];
    if (!p?.ns || !p?.kind || !p?.name) continue;
    watchers.set(key, {
      ns: p.ns,
      kind: p.kind,
      name: p.name,
      phase: "progressing",
      reason: "",
      reasonPod: "",
      ready: 0,
      desired: 0,
      fails: 0,
      warned: false,
      startedAt: p.startedAt || Date.now(),
    });
    schedule(key, 800);
  }
})();
