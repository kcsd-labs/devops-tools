import { useEffect, useState } from "react";

// The editor has to follow the portal's theme, which lives on the root element
// and is toggled from the top bar. Watching the attribute is what keeps the two
// in step without threading the theme through every page.
export function useEditorTheme(): "light" | "dark" {
  const read = () => (document.documentElement.dataset.theme === "light" ? "light" : "dark");
  const [theme, setTheme] = useState<"light" | "dark">(read);
  useEffect(() => {
    const obs = new MutationObserver(() => setTheme(read()));
    obs.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    return () => obs.disconnect();
  }, []);
  return theme;
}
