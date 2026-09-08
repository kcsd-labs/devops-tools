import { useEffect } from "react";

// Freezes the main scroll area while any modal is open.
//
// The counter lives at module level on purpose. Nested dialogs — a confirmation
// on top of the pod events window, say — can be closed in any order, and the
// scroll must only come back once the last one is gone. Storing the previous
// value per instance breaks exactly there: closing in reverse order restores
// someone else's "hidden" and the page stays stuck.
let locks = 0;

function apply() {
  const el = document.querySelector(".content") as HTMLElement | null;
  // Space for the scrollbar is already reserved (scrollbar-gutter: stable), so
  // hiding the overflow does not shift the content sideways.
  if (el) el.style.overflow = locks > 0 ? "hidden" : "";
}

export function useScrollLock(locked: boolean) {
  useEffect(() => {
    if (!locked) return;
    locks++;
    apply();
    return () => {
      locks--;
      apply();
    };
  }, [locked]);
}
