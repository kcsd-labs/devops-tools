import { useRef, useState } from "react";

const POP_WIDTH = 440; // approximate popup width, used to keep it on screen

export interface Preset {
  v: string; // id
  l: string; // label
  sec: number;
}

function today(): string {
  const d = new Date();
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

/** "2026-06-23T11:00:00" -> ["2026-06-23", "11:00:00"] */
function splitDateTime(s: string, defaultTime: string): [string, string] {
  if (!s) return [today(), defaultTime];
  const [date, time] = s.split("T");
  return [date || today(), time || defaultTime];
}

/**
 * Time range picker in the style of Grafana: quick presets on one side,
 * an absolute range on the other. The date comes from a native date picker and
 * the time is typed, which is far quicker than scrolling a combined widget.
 */
export function TimeRangePicker({
  presets,
  range,
  customFrom,
  customTo,
  onApply,
}: {
  presets: Preset[];
  range: string;
  customFrom: string;
  customTo: string;
  onApply: (range: string, from: string, to: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState({ top: 0, left: 0 });
  const btnRef = useRef<HTMLButtonElement>(null);
  const [fromDate, setFromDate] = useState(() => splitDateTime(customFrom, "00:00:00")[0]);
  const [fromTime, setFromTime] = useState(() => splitDateTime(customFrom, "00:00:00")[1]);
  const [toDate, setToDate] = useState(() => splitDateTime(customTo, "23:59:59")[0]);
  const [toTime, setToTime] = useState(() => splitDateTime(customTo, "23:59:59")[1]);

  const label =
    range === "custom"
      ? `${customFrom.replace("T", " ") || "…"} → ${customTo.replace("T", " ") || "…"}`
      : (presets.find((p) => p.v === range)?.l ?? range);

  const applyPreset = (v: string) => {
    onApply(v, customFrom, customTo);
    setOpen(false);
  };

  const valid = fromDate && fromTime && toDate && toTime;
  const applyAbsolute = () => {
    if (!valid) return;
    onApply("custom", `${fromDate}T${fromTime}`, `${toDate}T${toTime}`);
    setOpen(false);
  };

  const toggle = () => {
    if (!open && btnRef.current) {
      const r = btnRef.current.getBoundingClientRect();
      const left = Math.max(8, Math.min(r.left, window.innerWidth - POP_WIDTH - 12));
      setPos({ top: r.bottom + 6, left });
    }
    setOpen((v) => !v);
  };

  return (
    <div className="tr">
      <button ref={btnRef} className="input sm tr-btn" onClick={toggle} title="Time range">
        🕒 {label}
      </button>

      {open && (
        <>
          <div className="tr-backdrop" onClick={() => setOpen(false)} />
          <div className="tr-pop" style={{ top: pos.top, left: pos.left }}>
            <div className="tr-abs">
              <div className="tr-h">Absolute range</div>
              <div className="tr-field">
                <span>From</span>
                <div className="tr-dt">
                  <input
                    className="input sm"
                    type="date"
                    value={fromDate}
                    onChange={(e) => setFromDate(e.target.value)}
                  />
                  <input
                    className="input sm"
                    type="time"
                    step="1"
                    value={fromTime}
                    onChange={(e) => setFromTime(e.target.value)}
                  />
                </div>
              </div>
              <div className="tr-field">
                <span>To</span>
                <div className="tr-dt">
                  <input
                    className="input sm"
                    type="date"
                    value={toDate}
                    onChange={(e) => setToDate(e.target.value)}
                  />
                  <input
                    className="input sm"
                    type="time"
                    step="1"
                    value={toTime}
                    onChange={(e) => setToTime(e.target.value)}
                  />
                </div>
              </div>
              <button className="btn sm" onClick={applyAbsolute} disabled={!valid}>
                Apply
              </button>
            </div>

            <div className="tr-quick">
              <div className="tr-h">Quick ranges</div>
              {presets.map((p) => (
                <button
                  key={p.v}
                  className={"tr-quick-item " + (range === p.v ? "on" : "")}
                  onClick={() => applyPreset(p.v)}
                >
                  {p.l}
                </button>
              ))}
            </div>
          </div>
        </>
      )}
    </div>
  );
}
