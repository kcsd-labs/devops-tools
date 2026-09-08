// Background watcher for an image being rebuilt.
//
// Pressing "Rebuild image" starts a build that takes minutes, and the point of
// it is that the pod can pull its image again afterwards. Following that from
// outside React means the progress survives navigating away and a full page
// reload — which matters here more than elsewhere, because the natural thing to
// do while waiting is go and look at something else.
//
// Everything after the first call carries a ticket the server signed. The
// browser never names a project or a pipeline: it hands back what it was given.

import { pushToast } from "./toasts";
import { authHeaders } from "./session";
import { startRestartWatch } from "./restartWatcher";

export type RebuildState = {
  ns: string;
  pod: string;
  /** "build" retries one job; "pipeline" runs the whole thing, deploy included. */
  kind: "build" | "pipeline";
  phase: "running" | "restarting" | "waiting-for-person" | "done" | "failed";
  status: string; // GitLab's own status, shown as-is
  url: string; // the job or pipeline in GitLab, so the real thing is one click away
  error: string;
};

type Watcher = RebuildState & {
  ticket: string;
  timer?: number;
  fails: number;
  /** The manual gate is released once. Pressing it again on every poll would
      be pressing a button somebody else may have just pressed. */
  released: boolean;
  startedAt: number;
};

const watchers = new Map<string, Watcher>();
const subs = new Set<() => void>();

const POLL_MS = 5000;
const MAX_FAILS = 12; // ~1 minute of consecutive errors before giving up
const STORAGE_KEY = "devops-tools.rebuildWatchers.v1";

// GitLab's own vocabulary for "not finished".
const IN_PROGRESS = ["created", "pending", "running", "preparing", "waiting_for_resource", "scheduled"];

export const rebuildKey = (ns: string, pod: string) => `${ns}/${pod}`;

export function getRebuildStates(ns?: string): RebuildState[] {
  const out: RebuildState[] = [];
  watchers.forEach((w) => {
    if (!ns || w.ns === ns) {
      const { ticket, timer, fails, released, startedAt, ...state } = w;
      void ticket;
      void timer;
      void fails;
      void released;
      void startedAt;
      out.push(state);
    }
  });
  return out;
}

export function getRebuild(ns: string, pod: string): RebuildState | null {
  const w = watchers.get(rebuildKey(ns, pod));
  if (!w) return null;
  const { ticket, timer, fails, released, startedAt, ...state } = w;
  void ticket;
  void timer;
  void fails;
  void released;
  void startedAt;
  return state;
}

export function subscribeRebuilds(fn: () => void): () => void {
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
    const obj: Record<string, Pick<Watcher, "ns" | "pod" | "kind" | "ticket" | "released" | "startedAt">> = {};
    watchers.forEach((w, k) => {
      // Only work still in flight. A finished build has nothing to resume, and
      // its ticket is spent.
      if (w.phase === "running" || w.phase === "restarting") {
        obj[k] = { ns: w.ns, pod: w.pod, kind: w.kind, ticket: w.ticket, released: w.released, startedAt: w.startedAt };
      }
    });
    localStorage.setItem(STORAGE_KEY, JSON.stringify(obj));
  } catch {
    /* storage full or disabled — the watcher still works for this page */
  }
}

function patch(key: string, p: Partial<RebuildState>) {
  const w = watchers.get(key);
  if (!w) return;
  Object.assign(w, p);
  emit();
}

/** Start following a build job that was just retried. */
export function watchBuild(ns: string, pod: string, ticket: string, status: string, url: string) {
  start(ns, pod, "build", ticket, status, url);
}

/** Start following a fresh pipeline, deploy and all. */
export function watchPipeline(ns: string, pod: string, ticket: string, status: string, url: string) {
  start(ns, pod, "pipeline", ticket, status, url);
}

function start(ns: string, pod: string, kind: Watcher["kind"], ticket: string, status: string, url: string) {
  const key = rebuildKey(ns, pod);
  const existing = watchers.get(key);
  if (existing?.timer) window.clearTimeout(existing.timer);

  watchers.set(key, {
    ns,
    pod,
    kind,
    phase: "running",
    status,
    url,
    error: "",
    ticket,
    fails: 0,
    released: false,
    startedAt: Date.now(),
  });
  emit();
  schedule(key, 2000); // look once quickly, then settle into the interval
}

export function clearRebuildWatch(key: string) {
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

  const base = `/api/namespaces/${encodeURIComponent(w.ns)}/pods/${encodeURIComponent(w.pod)}/rebuild-image`;
  const url =
    w.kind === "pipeline"
      ? `${base}/pipeline/status?ticket=${encodeURIComponent(w.ticket)}`
      : `${base}/status?ticket=${encodeURIComponent(w.ticket)}`;

  try {
    const r = await fetch(url, { headers: authHeaders(), cache: "no-store" });
    if (r.status === 400 || r.status === 404) {
      // The ticket expired, or what it named is gone. Either way there is
      // nothing left to follow, and saying so beats polling into the void.
      fail(key, "Lost track of this build. Open it in GitLab to see how it ended.");
      return;
    }
    if (!r.ok) {
      if (registerFail(key)) return;
      schedule(key);
      return;
    }

    w.fails = 0;
    const d = await r.json();
    const status: string = d.status ?? d.jobStatus ?? "";
    patch(key, { status, url: d.pipelineUrl || d.jobUrl || w.url });

    if (status === "success") {
      if (w.kind === "pipeline") {
        // The pipeline deployed as well, so the workload is already being
        // replaced. Restarting on top of that would fight it.
        finish(key, `${w.pod}: the pipeline finished — the new version is on its way in.`);
      } else {
        // The image exists again, but the pod does not retry on its own for a
        // while: kubelet backs off between pull attempts, up to five minutes.
        // Leaving it there is leaving the job half done — the whole point was
        // to get the workload running.
        void restartAfterBuild(key);
      }
      return;
    }

    // The pipeline is waiting at its manual gate. Releasing it is a separate
    // permission, so this may come back refused — in which case the honest
    // thing is to say a person is needed, not to keep waiting silently.
    if (w.kind === "pipeline" && status === "manual") {
      if (!w.released) {
        w.released = true;
        void releaseDeploy(key);
      }
      schedule(key);
      return;
    }

    if (!IN_PROGRESS.includes(status)) {
      const failed: string[] = d.failedJobs ?? [];
      // Naming the job that failed is the difference between fixing something
      // and running the same build again to watch it fail the same way.
      fail(
        key,
        failed.length
          ? `Failed at ${failed.join(", ")}. Running it again will not help until that is dealt with.`
          : `Finished as ${status}.`
      );
      return;
    }
  } catch {
    if (registerFail(key)) return;
  }
  schedule(key);
}

// PUSH_SETTLE_MS lets the registry finish accepting the image. The job reports
// success as its last step completes, and a pull a moment later can still miss.
const PUSH_SETTLE_MS = 4000;

// Finish the job: restart the workload so it pulls the image that now exists.
//
// Handed over to the rollout watcher afterwards, so what follows is the same
// thing the Restart button produces — one mechanism, not two that behave
// slightly differently.
async function restartAfterBuild(key: string) {
  const w = watchers.get(key);
  if (!w) return;
  patch(key, { phase: "restarting", status: "" });

  await new Promise((r) => setTimeout(r, PUSH_SETTLE_MS));
  if (!watchers.has(key)) return; // cancelled while waiting

  try {
    const r = await fetch(
      `/api/namespaces/${encodeURIComponent(w.ns)}/pods/${encodeURIComponent(w.pod)}/restart`,
      { method: "POST", headers: authHeaders() }
    );
    if (r.status === 403) {
      // Building and restarting are separate permissions, and it is quite
      // reasonable to hold one without the other. The image is back either
      // way, which is the part that needed doing.
      finish(key, `${w.pod}: the image is back. Restart the pod to pick it up — your roles do not cover that.`);
      return;
    }
    if (!r.ok) {
      finish(key, `${w.pod}: the image is back, but the restart did not go through. Restart it by hand.`);
      return;
    }
    const d = await r.json();
    finish(key, `${w.pod}: image rebuilt, restarting the workload.`);
    if (d.kind && d.name) {
      // From here the rollout watcher reports readiness, a stuck rollout and
      // everything else it already knows how to explain.
      startRestartWatch(w.ns, d.kind, d.name);
    }
  } catch {
    finish(key, `${w.pod}: the image is back, but the restart request failed. Restart it by hand.`);
  }
}

async function releaseDeploy(key: string) {
  const w = watchers.get(key);
  if (!w) return;
  const url =
    `/api/namespaces/${encodeURIComponent(w.ns)}/pods/${encodeURIComponent(w.pod)}` +
    `/rebuild-image/pipeline/deploy?ticket=${encodeURIComponent(w.ticket)}`;
  try {
    const r = await fetch(url, { method: "POST", headers: authHeaders() });
    if (r.status === 403) {
      waitForPerson(key, "This pipeline is waiting on a manual deploy, which your roles do not cover. " +
        "Someone with deploy access can release it, here or in GitLab.");
      return;
    }
    if (!r.ok) {
      waitForPerson(key, "Could not release the deploy job — open the pipeline in GitLab.");
      return;
    }
    const d = await r.json();
    const played: string[] = d.played ?? [];
    const waiting: string[] = d.waiting ?? [];
    if (played.length) {
      pushToast(`Released ${played.join(", ")}`, "ok");
      return; // polling carries on and follows it to the end
    }
    if (waiting.length) {
      waitForPerson(key, `Waiting on ${waiting.join(", ")} — release it in GitLab.`);
      return;
    }
    // Nothing played and nothing waiting: somebody released it already,
    // between one poll and the next. Keep following.
  } catch {
    // A network error is not an answer. Let the next manual tick try again.
    const cur = watchers.get(key);
    if (cur) cur.released = false;
  }
}

function registerFail(key: string): boolean {
  const w = watchers.get(key);
  if (!w) return true;
  w.fails += 1;
  if (w.fails >= MAX_FAILS) {
    fail(key, "No answer while following this build. Check it in GitLab.");
    return true;
  }
  return false;
}

function fail(key: string, text: string) {
  const w = watchers.get(key);
  if (!w) return;
  patch(key, { phase: "failed", error: text });
  pushToast(`${w.pod}: ${text}`, "err", true);
  stop(key);
}

// Not a failure: the work is fine, it simply needs a person now. Kept apart so
// the banner can say that rather than showing an error for something that is
// waiting exactly as designed.
function waitForPerson(key: string, text: string) {
  const w = watchers.get(key);
  if (!w) return;
  patch(key, { phase: "waiting-for-person", error: text });
  pushToast(`${w.pod}: ${text}`, "warn", true);
  stop(key);
}

function finish(key: string, text: string) {
  patch(key, { phase: "done", error: "" });
  pushToast(text, "ok");
  stop(key);
}

function stop(key: string) {
  const w = watchers.get(key);
  if (w?.timer) window.clearTimeout(w.timer);
  if (w) w.timer = undefined;
  persist(); // the outcome stays on screen; only the polling stops
}

// Resume after a full page reload. The ticket is what makes this possible: it
// carries everything the server needs, and it is the server's own signature.
(function rehydrate() {
  let stored: Record<string, Partial<Watcher>> = {};
  try {
    stored = JSON.parse(localStorage.getItem(STORAGE_KEY) || "{}") || {};
  } catch {
    return;
  }
  for (const key of Object.keys(stored)) {
    const p = stored[key];
    if (!p?.ns || !p?.pod || !p?.ticket) continue;
    watchers.set(key, {
      ns: p.ns,
      pod: p.pod,
      kind: p.kind === "pipeline" ? "pipeline" : "build",
      phase: "running",
      status: "",
      url: "",
      error: "",
      ticket: p.ticket,
      fails: 0,
      released: p.released ?? false,
      startedAt: p.startedAt || Date.now(),
    });
    schedule(key, 800);
  }
})();
