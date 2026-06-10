---
id = "flutter"
version = "1.0.0"
tier = "any"
stacks = ["flutter"]
project_shapes = []
description = "Flutter {{FLUTTER_VERSION}} + Dart {{DART_VERSION}} conventions: BLoC architecture, file layout, testing, and common pitfalls."
---

## File Layout

Use feature-first layout. Each feature owns its bloc, widgets, models, and repository.

```
lib/
  app/
    app.dart              # MaterialApp/CupertinoApp wiring
    router.dart           # GoRouter config
  core/
    theme/                # ThemeData, tokens
    widgets/              # shared, feature-agnostic widgets
    utils/                # pure Dart helpers
    constants/
  features/
    auth/
      bloc/
        auth_bloc.dart
        auth_event.dart
        auth_state.dart
      data/
        auth_repository.dart
        auth_service.dart
      ui/
        login_page.dart
        login_form.dart
  main.dart               # entry point — only DI setup and runApp
test/
  features/               # mirrors lib/features/ structure
  core/
integration_test/
  app_test.dart
```

`main.dart` wires dependency injection (via `get_it` or manual injection) and calls `runApp`. Route logic lives in `router.dart`, not in `main.dart`.

## Build Tooling

```bash
flutter run               # run on connected device / emulator
flutter build apk         # Android release APK
flutter build appbundle   # Android App Bundle (Play Store)
flutter build ipa         # iOS IPA (macOS only)
flutter build macos       # macOS desktop
flutter test              # unit + widget tests
flutter test --coverage   # with lcov coverage output
dart analyze              # static analysis
dart format lib/ test/    # format source
```

Pin the Flutter SDK channel in `.fvmrc` or `pubspec.yaml`. Use FVM (`fvm use {{FLUTTER_VERSION}}`) for consistent SDK versions across the team. Never rely on the system Flutter path in CI — always use the pinned version.

## BLoC Architecture

Use `flutter_bloc` as the state management solution. Every interactive feature has a BLoC (or Cubit for simpler cases).

```dart
// features/auth/bloc/auth_bloc.dart

import 'package:flutter_bloc/flutter_bloc.dart';
import '../data/auth_repository.dart';

part 'auth_event.dart';
part 'auth_state.dart';

class AuthBloc extends Bloc<AuthEvent, AuthState> {
  final AuthRepository _repository;

  AuthBloc({required AuthRepository repository})
      : _repository = repository,
        super(const AuthInitial()) {
    on<AuthLoginRequested>(_onLoginRequested);
  }

  Future<void> _onLoginRequested(
    AuthLoginRequested event,
    Emitter<AuthState> emit,
  ) async {
    emit(const AuthLoading());
    try {
      final user = await _repository.login(event.email, event.password);
      emit(AuthAuthenticated(user: user));
    } catch (e) {
      emit(AuthError(message: e.toString()));
    }
  }
}
```

Use `BlocBuilder` only when the widget depends on state. Use `BlocListener` for one-time side effects (navigation, snackbars). Use `BlocConsumer` only when you need both.

## Testing Convention

```bash
flutter test                             # all unit + widget tests
flutter test test/features/auth/        # specific feature
flutter test --coverage                  # lcov output
dart run coverage:format_coverage \
  --lcov --in=coverage/json \
  --out=coverage/lcov.info --report-on=lib
```

Every BLoC must have a unit test file using `bloc_test` package. Every widget that contains business logic must have a widget test using `flutter_test`.

```dart
// test/features/auth/bloc/auth_bloc_test.dart
import 'package:bloc_test/bloc_test.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:mockito/mockito.dart';

void main() {
  group('AuthBloc', () {
    blocTest<AuthBloc, AuthState>(
      'emits [AuthLoading, AuthAuthenticated] on successful login',
      build: () => AuthBloc(repository: MockAuthRepository()),
      act: (bloc) => bloc.add(AuthLoginRequested(email: 'x@x.com', password: 'pw')),
      expect: () => [isA<AuthLoading>(), isA<AuthAuthenticated>()],
    );
  });
}
```

## Lint & Format

```
# analysis_options.yaml
include: package:flutter_lints/flutter.yaml
linter:
  rules:
    avoid_print: true
    prefer_const_constructors: true
    prefer_const_declarations: true
    require_trailing_commas: true
```

Run `dart analyze` in CI — it is an error, not a warning gate. Zero analysis issues required before merge. Run `dart format --set-exit-if-changed lib/ test/` in CI to enforce formatting.

## Common Pitfalls

- **`BuildContext` across async gaps.** After an `await`, a `BuildContext` may no longer be mounted. Check `context.mounted` before using `context` after any `await`. The lint rule `use_build_context_synchronously` catches this.
- **Widget rebuilds from BLoC.** `BlocBuilder` rebuilds on every state change. Add a `buildWhen` condition if you only care about a subset of state changes.
- **`const` constructors.** Mark every widget constructor `const` where possible. Flutter skips rebuilding const widgets in the diff. The `prefer_const_constructors` lint enforces this.
- **Large images in assets.** Uncompressed PNG assets inflate the app bundle. Compress images with `pngcrush` or `cwebp` before adding to `assets/`. Declare only the asset directories you use in `pubspec.yaml`.
- **Missing `pubspec.yaml` version.** CI and EAS-equivalent (Shorebird, Fastlane) rely on the `version` field in `pubspec.yaml`. Keep it in sync with your git tags.

## Performance Notes

Use `const` widgets aggressively — they are the most impactful performance optimisation in Flutter. The Flutter DevTools "Rebuild Stats" view shows which widgets rebuild most often.

For long lists, use `ListView.builder` (lazy) not `ListView` (eager). For very long or dynamic lists, consider `sliver_*` widgets inside a `CustomScrollView` for finer control.

Profile on a physical device, not a simulator. The iOS simulator and Android emulator do not reflect real rendering performance.
