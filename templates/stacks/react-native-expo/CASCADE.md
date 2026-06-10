# Stack Template: React Native + Expo

**Tier:** APC · **Stack:** React Native with Expo SDK · **Language:** TypeScript strict

## Idiomatic Layout

```
src/
  screens/              # Route-level screen components
  components/           # Reusable UI components
    ui/                 # Primitive wrappers (Tamagui/NativeBase)
  hooks/                # Custom hooks (shared logic)
  lib/                  # Domain business logic
  utils/                # Pure functions
  services/             # API clients, native module wrappers
  navigation/           # React Navigation config + types
  types/                # Shared TypeScript types
  store/                # Zustand slices
  config/               # Constants, env config
  assets/               # Images, fonts (committed)
app/                    # Expo Router file-based routes (if used)
__tests__/              # Jest + React Native Testing Library
.cascade/               # AI working memory (gitignored)
```

## Modular Coding Patterns

- Platform-specific files: `Component.ios.tsx` / `Component.android.tsx` override base
- Navigation types: typed RootStackParamList in `navigation/types.ts`
- Native modules wrapped in `services/` — never called directly from components
- Expo constants in `config/constants.ts`; never import from `expo-constants` in components

## Key Commands

```bash
pnpm start              # Expo dev server
pnpm ios                # iOS simulator
pnpm android            # Android emulator
pnpm test               # Jest
pnpm typecheck          # tsc --noEmit
pnpm lint               # ESLint with RN plugin
eas build               # EAS cloud build
eas submit              # Submit to stores
```

## Engineering Rules

- `tsconfig.json`: `"strict": true`, `"jsx": "react-native"`
- Expo SDK version pinned in `package.json`; upgrade via `expo upgrade`
- Deep linking config in `app.json` → `expo.scheme`
- File ceiling: screens ≤200 lines, hooks ≤150 lines
- `.cascade/docs/MASTER-SCREENS.md` tracks every screen and navigation route

## Cross-Refs

- `.cascade/rules/frontend-stack-selection.md`
- `.cascade/rules/engineering-excellence.md`
- `.cascade/rules/master-lists-protocol.md`
