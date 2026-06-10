---
id = "react-native"
version = "1.0.0"
tier = "any"
stacks = ["react-native"]
project_shapes = []
description = "React Native with Expo SDK {{EXPO_SDK_VERSION}}+ conventions: file layout, EAS, testing, and common pitfalls."
---

## File Layout

Use Expo Router's file-based routing. Keep feature logic out of route files — routes are thin shell screens.

```
app/
  _layout.tsx           # root layout — navigation stack, providers
  (tabs)/               # tab group
    index.tsx           # home tab
    explore.tsx
  (auth)/               # auth-gated group
    login.tsx
  +not-found.tsx        # 404 screen
components/
  ui/                   # atomic, screen-agnostic components
  features/             # feature-scoped composites
hooks/                  # custom hooks
lib/                    # pure utilities
services/               # API calls, SDK wrappers
types/                  # shared TypeScript interfaces
assets/
  images/
  fonts/
```

Never put business logic directly in `app/` route files. The route file imports a feature component and renders it — that is all.

## Build Tooling

```bash
pnpm start          # Expo dev server (Metro bundler)
pnpm ios            # run on iOS simulator (macOS only)
pnpm android        # run on Android emulator
pnpm web            # run in browser (Expo Web)

# EAS Build
eas build --profile development --platform ios
eas build --profile development --platform android
eas build --profile production --platform all

# EAS Submit
eas submit --platform ios
eas submit --platform android
```

Pin `{{EXPO_SDK_VERSION}}` in `package.json` (`expo` package). All Expo-ecosystem packages (`expo-camera`, `expo-fs`, etc.) must be compatible with the pinned SDK version — run `npx expo install` to get the correct peer versions instead of `pnpm add`.

Use `pnpm` for all non-Expo package management. For Expo-managed packages, use `npx expo install <package>` which resolves the correct version for the current SDK.

## EAS Configuration

`eas.json` defines build profiles:

```json
{
  "cli": { "version": ">= {{EAS_CLI_VERSION}}" },
  "build": {
    "development": {
      "developmentClient": true,
      "distribution": "internal"
    },
    "preview": {
      "distribution": "internal"
    },
    "production": {}
  },
  "submit": {
    "production": {}
  }
}
```

Never run `eas build --profile production` without a tagged release commit. Development and preview builds go to internal testers; production goes to the App Store / Play Store.

## Testing Convention

```bash
pnpm test          # Jest (Expo preset)
pnpm test:watch    # Jest watch mode
pnpm test:coverage # Jest coverage
```

Expo uses Jest with `jest-expo` preset — not Vitest. The preset handles Metro transform automatically.

Test components with `@testing-library/react-native`. For native module dependencies (camera, location, etc.), mock them in `__mocks__/` or use Jest's `moduleNameMapper`.

Never test navigation state directly — test the UI that the navigation state produces. Use `@testing-library/react-native` and `renderRouter` from `expo-router/testing-library` for route-level tests.

## Lint & Format

```bash
pnpm lint          # ESLint with expo/expo config
pnpm format        # prettier --write .
pnpm typecheck     # tsc --noEmit
```

Use `eslint-config-expo` as the ESLint base. Add `react-hooks/exhaustive-deps` as an error. Enable `@typescript-eslint/strict` — React Native apps should be as type-safe as any other TypeScript project.

## Common Pitfalls

- **`useEffect` with navigation focus.** On mobile, screens are not unmounted when navigating away — they stay mounted in the background. Use `useFocusEffect` from Expo Router instead of `useEffect` for data fetching that should re-run when the screen comes into focus.
- **Large image assets.** Uncompressed images in `assets/images/` inflate the app binary. Use WebP for photos and SVG (via `react-native-svg`) for icons. Never commit images over 500 KB without compression.
- **Platform-specific code.** Use `Platform.select` or `.ios.tsx` / `.android.tsx` file extensions for platform-specific branches. Never nest `if (Platform.OS === 'ios')` inside a shared component — extract it.
- **Metro cache corruption.** If Metro shows stale errors after a dependency change, run `npx expo start --clear` to wipe the cache.
- **`expo-constants` vs environment variables.** App config values (API URLs, feature flags) go in `app.config.ts` under `extra`. Access them via `Constants.expoConfig.extra`. Never use `process.env` in React Native code — it is not guaranteed to work across all platforms.

## Performance Notes

Use `FlashList` from `@shopify/flash-list` instead of `FlatList` for long lists. FlatList re-renders cells on scroll; FlashList recycles them.

Avoid `console.log` in production. They are not stripped by default and add overhead. Use `hermes-engine` (the default JS engine) — it supports bytecode caching which improves startup time.

Run the Expo Go profiler or Flipper to identify slow renders before optimising. Target 60 fps for all scroll and animation interactions.
