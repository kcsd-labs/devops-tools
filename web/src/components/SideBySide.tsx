// Two versions of a configuration file, aligned line by line.
//
// "They differ" is only half an answer — the useful half is where. Aligning the
// two means the eye finds the changed line instead of scrolling both panes and
// counting.

import { align } from "../lib/lineDiff";

export function SideBySide({
  leftLabel,
  rightLabel,
  left,
  right,
}: {
  leftLabel: string;
  rightLabel: string;
  left: string;
  right: string;
}) {
  // The final newline is not a difference — the synchroniser drops it, and the
  // server ignores it when deciding whether the two have drifted. Showing it
  // here would contradict the badge above.
  const a = left.replace(/[\r\n]+$/, "").split("\n");
  const b = right.replace(/[\r\n]+$/, "").split("\n");
  const rows = align(a, b);
  const changed = rows.filter((r) => r.kind !== "same").length;

  return (
    <div className="sbs">
      <div className="sbs-head">
        <div className="sbs-col-head">{leftLabel}</div>
        <div className="sbs-col-head">{rightLabel}</div>
      </div>
      <div className="sbs-body">
        <table className="sbs-table">
          <tbody>
            {rows.map((r, i) => (
              <tr key={i} className={"sbs-row " + r.kind}>
                <td className="sbs-no">{r.leftNo ?? ""}</td>
                <td className="sbs-text">{r.left ?? ""}</td>
                <td className="sbs-no">{r.rightNo ?? ""}</td>
                <td className="sbs-text">{r.right ?? ""}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="muted sbs-foot">
        {changed === 0
          ? "No differences."
          : `${changed} ${changed === 1 ? "line differs" : "lines differ"}.`}
      </p>
    </div>
  );
}
