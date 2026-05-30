/**
 * Purpose: Thin wrapper around the `get_config` Tauri command.
 * Outputs: AppConfig object
 * SPORT: MASTER-LIBS.md — config
 */

import { invoke } from "@tauri-apps/api/core";

export interface AppConfig {
  theme: "light" | "dark" | "system";
  locale: string;
  update_channel: "stable" | "beta" | "nightly";
  inbox_paths: string[];
  rag_index_paths: string[];
  daemon_autostart: boolean;
}

export async function get(): Promise<AppConfig> {
  return invoke<AppConfig>("get_config");
}
