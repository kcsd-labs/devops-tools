import { useLayoutEffect, useRef } from "react";

/**
 * A textarea that grows to fit its content instead of scrolling internally.
 *
 * Secret values are often long; letting the field expand keeps the whole value
 * visible and stops the inner scroll from swallowing the wheel and freezing the
 * page behind it.
 */
export function AutoTextarea({
  value,
  onChange,
  readOnly,
  placeholder,
  className,
}: {
  value: string;
  onChange?: (v: string) => void;
  readOnly?: boolean;
  placeholder?: string;
  className?: string;
}) {
  const ref = useRef<HTMLTextAreaElement>(null);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.style.height = "auto";
    // With box-sizing: border-box the scroll height excludes the border, which
    // leaves the field a couple of pixels shorter than the input next to it and
    // misaligns the row. Add the border back.
    const border = el.offsetHeight - el.clientHeight;
    el.style.height = el.scrollHeight + border + "px";
  }, [value]);

  return (
    <textarea
      ref={ref}
      className={className}
      value={value}
      readOnly={readOnly}
      placeholder={placeholder}
      rows={1}
      spellCheck={false}
      onChange={onChange ? (e) => onChange(e.target.value) : undefined}
    />
  );
}
