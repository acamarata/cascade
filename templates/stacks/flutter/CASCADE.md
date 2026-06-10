# Stack Template: Flutter

**Tier:** APC · **Stack:** Flutter (Dart) · **Language:** Dart with static types

## Idiomatic Layout

```
lib/
  features/             # Feature-first organization
    auth/
      domain/           # Entities, value objects, repository interfaces
      data/             # Repository implementations, data sources
      presentation/     # Widgets, BLoCs/Cubits, ViewModels
    home/
      domain/
      data/
      presentation/
  shared/
    widgets/            # Reusable UI components
    utils/              # Pure helper functions
    services/           # Platform services, injected abstractions
    theme/              # ThemeData, colors, typography
    l10n/               # Localization ARB files
  main.dart
  app.dart              # App widget, router, DI setup
test/                   # Mirrors lib/ structure
integration_test/       # Integration and E2E tests
.cascade/               # AI working memory (gitignored)
```

## Modular Coding Patterns

- BLoC/Cubit for state; never put business logic in widgets
- Repository pattern: interface in `domain/`, implementation in `data/`
- Dependency injection via `get_it` or `riverpod`
- Platform-specific code in `services/`; never call platform channels in widgets
- Flavors: `--dart-define-from-file=config/dev.json` per environment

## Key Commands

```bash
flutter pub get         # Install dependencies
flutter run             # Run on connected device/emulator
flutter test            # Unit + widget tests
flutter test integration_test/   # Integration tests
dart analyze            # Static analysis
dart format .           # Format
flutter build apk       # Android release
flutter build ipa       # iOS release
```

## Engineering Rules

- `analysis_options.yaml`: extends `flutter_lints`, treat warnings as errors in CI
- No `dynamic` without justification comment
- File ceiling: widgets ≤200 lines, BLoCs ≤300 lines; split by concern beyond limits
- Localization: all user-visible strings in `.arb` files; zero hardcoded strings in widgets

## Cross-Refs

- `.cascade/rules/engineering-excellence.md`
- `.cascade/rules/master-lists-protocol.md`
