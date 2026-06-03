# E-04: macOS Widget & Menubar — Component Registry (SPORT)

**Phase:** P2 · **Epic:** E-04 — macOS Widget & Menubar  
**Status:** ✅ Complete (all components shipped)  
**Updated:** 2026-06-03 (T-P2-E04-13)

---

## Component Inventory

All components built in E-04 are listed below with location, status, and brief description.

| # | Component | File Path | Status | Description |
|---|---|---|---|---|
| 1 | CacheModel | `CascadeWidgetExtension/CacheModel.swift` | ✅ Done | P2 schema: Codable struct for App Group cache.json parsing; supports WidgetData, cascade_tiers, and staleness detection (120s threshold) |
| 2 | CascadeWidgetProvider | `CascadeWidgetExtension/Provider.swift` | ✅ Done | TimelineProvider conformance; 30-second refresh timeline; supplies WidgetData to views |
| 3 | SmallView | `CascadeWidgetExtension/Views/SmallView.swift` | ✅ Done | 140×140 WidgetKit view; displays inbox_unread count with dynamic color; Text + SF Symbol |
| 4 | MediumView | `CascadeWidgetExtension/Views/MediumView.swift` | ✅ Done | 154×154 WidgetKit view; shows CascadeTiers (GCI/ASI/PPI rules/refs/memory); TierRow structure |
| 5 | LargeView | `CascadeWidgetExtension/Views/LargeView.swift` | ✅ Done | 154×195 WidgetKit view; embeds MediumView + additional detail panels |
| 6 | MonochromeDimModifier | `CascadeWidgetExtension/Modifiers/MonochromeDimModifier.swift` | ✅ Done | SwiftUI modifier applying widgetRenderingMode .accented when desktop is inactive |
| 7 | MenubarController | `CascadeApp/MenubarController.swift` | ✅ Done | NSStatusItem + NSPopover; updateIcon() shows green/amber/offline states; owns 30s refresh timer |
| 8 | MenubarPopoverView | `CascadeApp/MenubarPopoverView.swift` | ✅ Done | SwiftUI 280×180 popover; shows daemon health, project count, task totals; buttons for Open Cascade + Refresh |
| 9 | CascadeWidgetTests | `CascadeWidgetTests/CascadeWidgetTests.swift` | ✅ Done | 8 XCTest unit tests: CacheModel decode, isStale computation, ageString formatting |

---

## Key Architecture Notes

- **App Group:** `group.io.cascade` (shared container for WidgetKit + menubar app)
- **Cache location:** `~/Library/Group Containers/group.io.cascade/cache.json`
- **Staleness:** 120 seconds (isStale flag set if generated_at < now - 120s)
- **Menubar integration:** NSStatusItem targets CascadeApp; WidgetCenter.shared for timeline reloads
- **CI:** GitHub Actions on macos-14 runner with xcodegen + xcodebuild test
- **Provisioning:** Apple Developer Team ID required; documented in USER-AUTH-GATES.md

---

## Status Summary

✅ **All E-04 components complete.** Ready for P2 Build phase completion and transition to P3.
