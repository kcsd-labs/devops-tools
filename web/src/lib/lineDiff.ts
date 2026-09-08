// Pairing two versions of a file line by line.
//
// Kept apart from the view so it can be exercised directly: this is the part
// that can be subtly wrong — an inserted line that shifts everything below it
// into a wall of differences still "works", it is just useless.

export type Row = {
  left?: string;
  right?: string;
  leftNo?: number;
  rightNo?: number;
  kind: "same" | "changed" | "left-only" | "right-only";
};

// Beyond this, the alignment is done coarsely rather than exactly. A quadratic
// match over a file this size would lock the browser, and nobody reads a
// thousand changed lines one at a time anyway.
const EXACT_LIMIT = 600;

// align pairs the lines up.
//
// The common start and end are matched off first. In practice that is almost
// everything — configuration files drift by a line or two — which leaves the
// expensive part with almost nothing to do.
export function align(a: string[], b: string[]): Row[] {
  let start = 0;
  while (start < a.length && start < b.length && a[start] === b[start]) start++;

  let endA = a.length - 1;
  let endB = b.length - 1;
  while (endA >= start && endB >= start && a[endA] === b[endB]) {
    endA--;
    endB--;
  }

  const rows: Row[] = [];
  for (let i = 0; i < start; i++) {
    rows.push({ left: a[i], right: b[i], leftNo: i + 1, rightNo: i + 1, kind: "same" });
  }

  const midA = a.slice(start, endA + 1);
  const midB = b.slice(start, endB + 1);
  for (const r of alignMiddle(midA, midB, start)) rows.push(r);

  for (let i = endA + 1; i < a.length; i++) {
    rows.push({
      left: a[i],
      right: b[i - (endA + 1) + (endB + 1)],
      leftNo: i + 1,
      rightNo: i - (endA + 1) + (endB + 1) + 1,
      kind: "same",
    });
  }
  return rows;
}

function alignMiddle(a: string[], b: string[], offset: number): Row[] {
  const rows: Row[] = [];
  const push = (li: number, ri: number, kind: Row["kind"]) =>
    rows.push({
      left: li >= 0 ? a[li] : undefined,
      right: ri >= 0 ? b[ri] : undefined,
      leftNo: li >= 0 ? offset + li + 1 : undefined,
      rightNo: ri >= 0 ? offset + ri + 1 : undefined,
      kind,
    });

  if (a.length === 0 && b.length === 0) return rows;

  // Too large to match exactly: put the blocks side by side and let the reader
  // see the shape of it. Still honest — nothing is claimed to be the same.
  if (a.length > EXACT_LIMIT || b.length > EXACT_LIMIT) {
    for (let i = 0; i < Math.max(a.length, b.length); i++) {
      push(i < a.length ? i : -1, i < b.length ? i : -1, "changed");
    }
    return rows;
  }

  // Longest common subsequence over lines, then walked back to produce the
  // pairing. This is what makes an inserted line show as an insertion rather
  // than shifting everything below it into a wall of red.
  const n = a.length;
  const m = b.length;
  const lcs: number[][] = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i][j] = a[i] === b[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
    }
  }

  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      push(i, j, "same");
      i++;
      j++;
    } else if (lcs[i + 1][j] >= lcs[i][j + 1]) {
      // A line present only on the left. Pairing it with the next right-only
      // line reads far better than two separate rows, so that is done below.
      const rightOnlyNext = lcs[i + 1][j] === lcs[i][j + 1];
      if (rightOnlyNext && j < m) {
        push(i, j, "changed");
        i++;
        j++;
      } else {
        push(i, -1, "left-only");
        i++;
      }
    } else {
      push(-1, j, "right-only");
      j++;
    }
  }
  while (i < n) {
    push(i, -1, "left-only");
    i++;
  }
  while (j < m) {
    push(-1, j, "right-only");
    j++;
  }
  return rows;
}
