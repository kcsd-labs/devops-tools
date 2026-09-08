import { useState } from "react";
import { useScrollLock } from "../useScrollLock";
import { pushToast } from "../toasts";

// Replaces the browser's confirm() and alert() with dialogs that match the rest
// of the UI — and, unlike the native ones, do not block the page.
//
//   const { dialogs, confirm, alertDlg, toast } = useDialogs();
//   if (!(await confirm({ message: "Are you sure?" }))) return;
//   ...and render {dialogs} somewhere in the page.

type ConfirmOpts = {
  title?: string;
  message: string;
  /** Label of the confirming button; defaults to "Confirm". */
  okText?: string;
  /** Renders the confirming button in red, for irreversible actions. */
  danger?: boolean;
};

type DialogState =
  | { kind: "confirm"; opts: ConfirmOpts; resolve: (ok: boolean) => void }
  | { kind: "alert"; opts: { title?: string; message: string }; resolve: () => void };

export function useDialogs() {
  const [dlg, setDlg] = useState<DialogState | null>(null);
  useScrollLock(!!dlg);

  const confirm = (opts: ConfirmOpts) =>
    new Promise<boolean>((resolve) => setDlg({ kind: "confirm", opts, resolve }));

  const alertDlg = (message: string, title?: string) =>
    new Promise<void>((resolve) => setDlg({ kind: "alert", opts: { message, title }, resolve }));

  /** Brief, non-blocking feedback such as "Copied to clipboard". */
  const toast = (message: string) => pushToast(message, "ok");

  const close = (ok: boolean) => {
    if (!dlg) return;
    setDlg(null);
    if (dlg.kind === "confirm") dlg.resolve(ok);
    else dlg.resolve();
  };

  const dialogs = (
    <>
      {dlg && (
        <div className="modal-overlay dialog-overlay" onClick={() => close(false)}>
          <div className="modal dialog-sm" onClick={(e) => e.stopPropagation()}>
            <div className="modal-head">
              <div className="modal-title">
                {dlg.opts.title ?? (dlg.kind === "confirm" ? "Please confirm" : "Notice")}
              </div>
              <button className="btn ghost sm" onClick={() => close(false)}>
                ✕
              </button>
            </div>
            <div className="modal-body pad dialog-msg">{dlg.opts.message}</div>
            <div className="dialog-actions">
              {dlg.kind === "confirm" ? (
                <>
                  <button className="btn ghost" onClick={() => close(false)}>
                    Cancel
                  </button>
                  <button
                    className={"btn" + (dlg.opts.danger ? " danger" : "")}
                    onClick={() => close(true)}
                    autoFocus
                  >
                    {dlg.opts.okText ?? "Confirm"}
                  </button>
                </>
              ) : (
                <button className="btn" onClick={() => close(true)} autoFocus>
                  OK
                </button>
              )}
            </div>
          </div>
        </div>
      )}
    </>
  );

  return { dialogs, confirm, alertDlg, toast };
}
