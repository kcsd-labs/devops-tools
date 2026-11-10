import { useCallback, useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { useApi } from "../useApi";
import { useDialogs } from "./Dialogs";

// A shell in a container, as a mirror. The terminal above only shows what the
// shell prints; nothing typed there reaches it. Commands are written in the
// line below and sent whole, and the server records each one before passing it
// on — so what the trail says ran is what ran.

type Phase = "idle" | "connecting" | "open" | "closed";

// A console, dark in both themes: what a shell prints assumes a dark
// background, and colours chosen for one look broken on the other.
const THEME = {
  background: "#0b1020",
  foreground: "#d7deea",
  cursor: "#0b1020", // the shell's cursor would suggest typing there
  selectionBackground: "#2b3a5c",
};

function minutesLeft(until: number): string {
  const m = Math.max(0, Math.round((until - Date.now()) / 60000));
  return m >= 60 ? `${Math.floor(m / 60)} h ${m % 60} min` : `${m} min`;
}

export default function PodTerminal({
  namespace,
  pod,
  containers,
}: {
  namespace: string;
  pod: string;
  containers: string[];
}) {
  const api = useApi();
  const { dialogs, confirm } = useDialogs();
  const [container, setContainer] = useState(containers[0] ?? "");
  const [phase, setPhase] = useState<Phase>("idle");
  const [error, setError] = useState("");
  const [refused, setRefused] = useState("");
  const [closedReason, setClosedReason] = useState("");
  const [shell, setShell] = useState("");
  const [until, setUntil] = useState(0);
  const [idleMinutes, setIdleMinutes] = useState(0);
  const [, setTick] = useState(0);
  const [line, setLine] = useState("");

  const hostRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  // This session's lines, newest last, and where ↑/↓ currently points.
  const history = useRef<string[]>([]);
  const cursor = useRef(-1);

  useEffect(() => {
    if (!containers.includes(container)) setContainer(containers[0] ?? "");
  }, [containers, container]);

  // The terminal lives as long as the tab: a closed session's output stays
  // readable until the next one starts.
  useEffect(() => {
    const term = new Terminal({
      theme: THEME,
      fontFamily: '"JetBrains Mono", ui-monospace, "Cascadia Code", monospace',
      fontSize: 13,
      disableStdin: true,
      convertEol: false,
      scrollback: 5000,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(hostRef.current!);
    fit.fit();
    termRef.current = term;
    fitRef.current = fit;

    const ro = new ResizeObserver(() => {
      fit.fit();
      const ws = wsRef.current;
      if (ws?.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      }
    });
    ro.observe(hostRef.current!);
    return () => {
      ro.disconnect();
      wsRef.current?.close();
      term.dispose();
    };
  }, []);

  // The countdown in the status line.
  useEffect(() => {
    if (phase !== "open") return;
    const t = setInterval(() => setTick((n) => n + 1), 30000);
    return () => clearInterval(t);
  }, [phase]);

  const connect = useCallback(async () => {
    setError("");
    setRefused("");
    setClosedReason("");
    setPhase("connecting");
    let t;
    try {
      t = await api.execTicket(namespace, pod, container);
    } catch (e: any) {
      setError(e.message);
      setPhase("idle");
      return;
    }
    const term = termRef.current!;
    term.reset();
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${proto}://${location.host}/api/exec?ticket=${encodeURIComponent(t.ticket)}`);
    ws.binaryType = "arraybuffer";
    wsRef.current = ws;
    history.current = [];
    cursor.current = -1;

    ws.onmessage = (ev) => {
      if (typeof ev.data !== "string") {
        term.write(new Uint8Array(ev.data as ArrayBuffer));
        return;
      }
      const m = JSON.parse(ev.data);
      if (m.type === "ready") {
        setShell(m.shell);
        setUntil(m.until);
        setIdleMinutes(Math.round(m.idleSeconds / 60));
        setPhase("open");
        fitRef.current?.fit();
        ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
        setTimeout(() => inputRef.current?.focus(), 0);
      } else if (m.type === "refused") {
        setRefused(m.message);
      } else if (m.type === "closed") {
        setClosedReason(m.reason);
      }
    };
    ws.onclose = () => {
      setPhase("closed");
      wsRef.current = null;
    };
    ws.onerror = () => setError("The connection to the shell failed.");
  }, [api, namespace, pod, container]);

  const send = (m: object) => {
    const ws = wsRef.current;
    if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify(m));
  };

  const sendLine = (text: string) => {
    setRefused("");
    send({ type: "line", text });
    if (text.trim() && history.current[history.current.length - 1] !== text) history.current.push(text);
    cursor.current = -1;
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    const input = e.currentTarget;
    if (e.key === "Enter") {
      e.preventDefault();
      sendLine(line);
      setLine("");
    } else if (e.key === "ArrowUp" || e.key === "ArrowDown") {
      const h = history.current;
      if (h.length === 0) return;
      e.preventDefault();
      let i = cursor.current;
      if (e.key === "ArrowUp") i = i === -1 ? h.length - 1 : Math.max(0, i - 1);
      else i = i === -1 ? -1 : i + 1 >= h.length ? -1 : i + 1;
      cursor.current = i;
      setLine(i === -1 ? "" : h[i]);
    } else if (e.ctrlKey && e.key === "c" && input.selectionStart === input.selectionEnd) {
      // Copy when something is selected; interrupt when nothing is.
      e.preventDefault();
      send({ type: "key", key: "c" });
    } else if (e.ctrlKey && e.key === "d" && line === "") {
      e.preventDefault();
      send({ type: "key", key: "d" });
    } else if (e.ctrlKey && e.key === "l") {
      e.preventDefault();
      termRef.current?.clear();
    }
  };

  // Several lines pasted at once are several commands, each with its own
  // record. Said before they go, since they cannot be called back.
  const onPaste = async (e: React.ClipboardEvent<HTMLInputElement>) => {
    const text = e.clipboardData.getData("text").replace(/\r\n?/g, "\n");
    if (!text.includes("\n")) return;
    e.preventDefault();
    const lines = text.replace(/\n$/, "").split("\n");
    const ok = await confirm({
      title: `Send ${lines.length} lines?`,
      message: "Each line runs as a separate command, in order, and each is recorded.",
      okText: "Send",
    });
    if (!ok) return;
    for (const l of lines) sendLine(l);
    inputRef.current?.focus();
  };

  const open = phase === "open";

  return (
    <div className="term">
      {dialogs}
      <div className="term-bar">
        {containers.length > 1 && (
          <select
            className="input sm select"
            value={container}
            disabled={phase === "open" || phase === "connecting"}
            onChange={(e) => setContainer(e.target.value)}
            title="Container"
          >
            {containers.map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </select>
        )}
        {open ? (
          <>
            <span className="term-live">● {shell}</span>
            <span className="muted">
              ends in {minutesLeft(until)} · closes after {idleMinutes} min without input
            </span>
            <span style={{ marginLeft: "auto" }} />
            <button className="btn ghost sm" onClick={() => wsRef.current?.close()}>
              Disconnect
            </button>
          </>
        ) : (
          <>
            <button className="btn sm" onClick={connect} disabled={phase === "connecting" || !container}>
              {phase === "connecting" ? "Connecting…" : phase === "closed" ? "Reconnect" : "Connect"}
            </button>
            {phase === "closed" && closedReason && <span className="muted">Closed: {closedReason}.</span>}
          </>
        )}
      </div>

      <div className="term-notice">Every line you send is recorded in the audit trail before it runs.</div>
      {error && <div className="alert">{error}</div>}

      <div className="term-screen" ref={hostRef} onClick={() => open && inputRef.current?.focus()} />

      <div className={"term-input" + (open ? "" : " off")}>
        <span className="term-prompt">›</span>
        <input
          ref={inputRef}
          className="mono"
          value={line}
          disabled={!open}
          spellCheck={false}
          autoComplete="off"
          placeholder={open ? "Type a command and press Enter" : "Connect to send commands"}
          onChange={(e) => setLine(e.target.value)}
          onKeyDown={onKeyDown}
          onPaste={onPaste}
          aria-label="Command"
        />
        <button className="btn ghost sm" disabled={!open} onClick={() => send({ type: "key", key: "c" })} title="Interrupt the running command">
          Ctrl+C
        </button>
        <button className="btn ghost sm" disabled={!open} onClick={() => send({ type: "key", key: "d" })} title="End of input">
          Ctrl+D
        </button>
      </div>
      {refused && <div className="term-refused">Not sent: {refused}</div>}
      <div className="term-help muted">
        ↑ ↓ history · Ctrl+C interrupts · Ctrl+L clears the screen · full-screen programs (vim, top, less) do
        not work here: use cat, head, ps
      </div>
    </div>
  );
}
