import { useEffect, useState } from "react";

// Global toasts. pushToast can be called from anywhere — including the
// background watchers that live outside React — and the notification shows up
// on whatever page the user happens to be on.
//
//   ok    disappears on its own
//   warn  stays longer
//   err   sticky: only closes when dismissed

export type Toast = { id: number; text: string; kind: "ok" | "warn" | "err"; sticky?: boolean };

let nextId = 1;
let items: Toast[] = [];
const subs = new Set<(t: Toast[]) => void>();

function emit() {
  subs.forEach((fn) => fn(items));
}

export function pushToast(text: string, kind: Toast["kind"] = "ok", sticky = false) {
  const t: Toast = { id: nextId++, text, kind, sticky };
  items = [...items, t];
  emit();
  if (!sticky) {
    window.setTimeout(() => dismissToast(t.id), kind === "ok" ? 6000 : 12000);
  }
}

export function dismissToast(id: number) {
  items = items.filter((t) => t.id !== id);
  emit();
}

/** Rendered once by Layout, so toasts survive navigation between pages. */
export function GlobalToasts() {
  const [list, setList] = useState<Toast[]>(items);
  useEffect(() => {
    subs.add(setList);
    return () => {
      subs.delete(setList);
    };
  }, []);
  if (!list.length) return null;
  return (
    <div className="toast-stack">
      {list.map((t) => (
        <div key={t.id} className={"toast-item t-" + t.kind}>
          <span>{t.text}</span>
          <button className="toast-x" onClick={() => dismissToast(t.id)} title="Dismiss">
            ✕
          </button>
        </div>
      ))}
    </div>
  );
}
