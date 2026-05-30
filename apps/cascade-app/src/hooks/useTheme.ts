/**
 * Purpose: Theme state — reads from and writes to `~/.cascade/config.json`
 *   via the `get_config` / `set_config` Tauri commands.
 * Outputs: { theme, setTheme }
 * Constraints: Falls back to "system" if config is unavailable on first render.
 * SPORT: MASTER-HOOKS.md — useTheme
 */

import { useCallback, useEffect, useState } from "react";
import { invoke } from "@tauri-apps/api/core";

type Theme = "light" | "dark" | "system";

export function useTheme() {
  const [theme, setThemeState] = useState<Theme>("system");

  useEffect(() => {
    invoke<{ theme: Theme }>("get_config")
      .then((c) => setThemeState(c.theme))
      .catch(() => {/* use default "system" */});
  }, []);

  const setTheme = useCallback((t: Theme) => {
    setThemeState(t);
    invoke("set_config", { key: "theme", value: t }).catch(console.error);
  }, []);

  return { theme, setTheme };
}
