import { useState } from "react";
import type { MetricPoint } from "../api";
import { useScrollLock } from "../useScrollLock";

// Lightweight single-series SVG chart: line, fill, limit marker, hover crosshair
// and a full-screen expand. No charting library — this is all it needs to do.
export function Chart({
  title,
  points,
  color = "#4ea1ff",
  limit,
  format,
}: {
  title: string;
  points: MetricPoint[] | null | undefined;
  color?: string;
  limit?: number;
  format: (v: number) => string;
}) {
  const W = 600;
  const H = 170;
  const PADL = 56;
  const PADR = 10;
  const PADT = 10;
  const PADB = 22;

  const [hover, setHover] = useState<number | null>(null);
  const [big, setBig] = useState(false);
  useScrollLock(big); // keep the page still while the chart is expanded

  const pts = points ?? [];
  const current = pts.length ? pts[pts.length - 1].v : 0;

  if (!pts.length) {
    return (
      <div className="chart-box">
        <div className="chart-head">
          <span className="chart-title">{title}</span>
          <span className="muted">no data</span>
        </div>
        <div className="chart-empty muted">— no data for this range —</div>
      </div>
    );
  }

  const minT = pts[0].t;
  const maxT = pts[pts.length - 1].t;
  // The scale always includes the limit, so the line shows real headroom: a low
  // line means the workload is far from its limit, rather than being rescaled
  // to fill the chart and looking alarming.
  const maxV = Math.max(limit ?? 0, ...pts.map((p) => p.v)) * 1.1 || 1;

  const x = (t: number) =>
    PADL + (maxT === minT ? 0 : (t - minT) / (maxT - minT)) * (W - PADL - PADR);
  const y = (v: number) => PADT + (1 - v / maxV) * (H - PADT - PADB);

  const line = pts
    .map((p, i) => `${i ? "L" : "M"}${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`)
    .join(" ");
  const area = `${line} L${x(maxT).toFixed(1)},${y(0).toFixed(1)} L${x(minT).toFixed(1)},${y(0).toFixed(1)} Z`;
  const gridVals = [0, 0.5, 1].map((f) => maxV * f);

  const onMove = (e: React.MouseEvent<SVGSVGElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
    const vbX = ((e.clientX - rect.left) / rect.width) * W;
    let best = 0;
    let bestD = Infinity;
    for (let i = 0; i < pts.length; i++) {
      const d = Math.abs(x(pts[i].t) - vbX);
      if (d < bestD) {
        bestD = d;
        best = i;
      }
    }
    setHover(best);
  };

  const hp = hover !== null ? pts[hover] : null;

  const body = (isBig: boolean) => (
    <>
      <div className="chart-head">
        <span className="chart-title">{title}</span>
        <div className="chart-head-right">
          <span className="chart-current">
            <span className="chart-now-label">now</span>
            <span style={{ color }}>{format(current)}</span>
          </span>
          <button
            className="chart-expand"
            title={isBig ? "Collapse" : "Expand"}
            onClick={() => setBig(!isBig)}
          >
            {isBig ? "✕" : "⤢"}
          </button>
        </div>
      </div>

      <div className={"chart-plot" + (isBig ? " big" : "")}>
        <svg
          viewBox={`0 0 ${W} ${H}`}
          className="chart"
          preserveAspectRatio="none"
          onMouseMove={onMove}
          onMouseLeave={() => setHover(null)}
        >
          {gridVals.map((v, i) => (
            <g key={i}>
              <line x1={PADL} x2={W - PADR} y1={y(v)} y2={y(v)} className="chart-grid" />
              <text x={PADL - 6} y={y(v) + 3} className="chart-ylabel">
                {format(v)}
              </text>
            </g>
          ))}

          {limit !== undefined && limit > 0 && (
            <g>
              <line x1={PADL} x2={W - PADR} y1={y(limit)} y2={y(limit)} className="chart-limit" />
              <line x1={PADL} x2={W - PADR} y1={y(limit)} y2={y(limit)} stroke="transparent" strokeWidth={10}>
                <title>{`Limit: ${format(limit)}`}</title>
              </line>
            </g>
          )}

          <path d={area} fill={color} fillOpacity={0.12} />
          <path d={line} fill="none" stroke={color} strokeWidth={1.5} />

          {hp && (
            <g>
              <line x1={x(hp.t)} x2={x(hp.t)} y1={PADT} y2={H - PADB} className="chart-cross" />
              <circle cx={x(hp.t)} cy={y(hp.v)} r={3.5} fill={color} stroke="var(--bg)" strokeWidth={1.5} />
            </g>
          )}
        </svg>

        {hp && (
          <div className="chart-tip" style={{ left: `${(x(hp.t) / W) * 100}%` }}>
            <div className="chart-tip-val" style={{ color }}>
              {format(hp.v)}
            </div>
            <div className="chart-tip-time">{new Date(hp.t * 1000).toLocaleTimeString()}</div>
          </div>
        )}
      </div>
    </>
  );

  return (
    <>
      <div className="chart-box">{body(false)}</div>
      {big && (
        <div className="modal-overlay" onClick={() => setBig(false)}>
          <div className="modal chart-modal" onClick={(e) => e.stopPropagation()}>
            {body(true)}
          </div>
        </div>
      )}
    </>
  );
}
