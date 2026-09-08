/** Small ⓘ icon with a tooltip, shown on hover or keyboard focus. */
export function InfoHint({ text }: { text: string }) {
  return (
    <span className="hint" tabIndex={0}>
      <span className="hint-icon">ⓘ</span>
      <span className="hint-bubble" role="tooltip">
        {text}
      </span>
    </span>
  );
}
