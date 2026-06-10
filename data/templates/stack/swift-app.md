---
id = "swift-app"
version = "1.0.0"
tier = "any"
stacks = ["swift", "swiftui", "ios", "macos"]
project_shapes = []
description = "Swift 5.10+ SwiftUI app: ViewModel/View split, XCTest, SwiftLint"
---

# CASCADE Instructions — Swift / SwiftUI App

> Stack: Swift 5.10+ · SwiftUI · XCTest · SwiftLint
> Tier: any (typically PAC for an app target)

Use `{{app_name}}` for the app target name, `{{bundle_id}}` for the bundle identifier prefix (e.g. `com.acme`), and `{{min_platform}}` for the minimum OS (e.g. `iOS 17.0` or `macOS 14.0`).

---

## Module / Package Layout

Xcode project structure. Keep source under `{{app_name}}/`:

```
{{app_name}}/
├── {{app_name}}/
│   ├── App/
│   │   ├── {{app_name}}App.swift     # @main entry point
│   │   └── ContentView.swift
│   ├── Features/
│   │   └── Dashboard/
│   │       ├── DashboardView.swift
│   │       └── DashboardViewModel.swift
│   ├── Shared/
│   │   ├── Components/               # reusable SwiftUI views
│   │   ├── Extensions/
│   │   └── Utilities/
│   ├── Resources/
│   │   └── Assets.xcassets
│   └── Info.plist
├── {{app_name}}Tests/
│   └── DashboardViewModelTests.swift
├── {{app_name}}UITests/
│   └── {{app_name}}UITests.swift
├── .swiftlint.yml
└── Package.swift                     # if using Swift Package Manager modules
```

---

## Build & Tooling

Build via Xcode or `xcodebuild`:

```bash
# Build for simulator
xcodebuild -scheme {{app_name}} \
           -destination 'platform=iOS Simulator,name=iPhone 15' \
           build

# Run unit tests
xcodebuild test \
           -scheme {{app_name}} \
           -destination 'platform=iOS Simulator,name=iPhone 15'

# Lint
swiftlint lint

# Auto-correct lint violations
swiftlint --fix
```

For CI, add `set -o pipefail` and pipe to `xcbeautify` or `xcpretty` for readable output.

---

## Testing Convention

Use `XCTest` for unit tests. Test ViewModels, not Views.

```swift
// {{app_name}}Tests/DashboardViewModelTests.swift
import XCTest
@testable import {{app_name}}

final class DashboardViewModelTests: XCTestCase {

    func testInitialStateIsEmpty() {
        let vm = DashboardViewModel()
        XCTAssertTrue(vm.items.isEmpty)
        XCTAssertFalse(vm.isLoading)
    }

    func testLoadItemsPopulatesArray() async throws {
        let vm = DashboardViewModel(service: MockService())
        await vm.loadItems()
        XCTAssertFalse(vm.items.isEmpty)
    }
}
```

- Inject dependencies via `init` parameters so you can substitute mocks in tests.
- `@testable import {{app_name}}` to access `internal` symbols.
- UI tests in `{{app_name}}UITests` use `XCUIApplication`; keep them minimal and deterministic.

---

## Lint & Format

SwiftLint enforces style. Minimum `.swiftlint.yml`:

```yaml
disabled_rules:
  - trailing_whitespace    # handled by Xcode auto-format

opt_in_rules:
  - force_unwrapping
  - implicitly_unwrapped_optional
  - explicit_type_interface

excluded:
  - DerivedData
  - .build

line_length: 120
```

Run `swiftlint lint --strict` in CI. Zero warnings policy.

---

## SwiftUI View Decomposition

Split every feature into a **ViewModel** (observable state + logic) and a **View** (pure rendering).

```swift
// DashboardViewModel.swift
@MainActor
final class DashboardViewModel: ObservableObject {
    @Published var items: [Item] = []
    @Published var isLoading = false

    private let service: ItemServiceProtocol

    init(service: ItemServiceProtocol = ItemService()) {
        self.service = service
    }

    func loadItems() async {
        isLoading = true
        defer { isLoading = false }
        items = await service.fetchItems()
    }
}

// DashboardView.swift
struct DashboardView: View {
    @StateObject private var vm = DashboardViewModel()

    var body: some View {
        List(vm.items) { item in ItemRow(item: item) }
            .task { await vm.loadItems() }
    }
}
```

Rules:
- ViewModels own all `@Published` state. Views are rendering-only.
- Use a protocol for the service so tests can inject a mock.
- `@MainActor` on ViewModels ensures state mutations happen on the main thread.

---

## Common Pitfalls

- **`@StateObject` vs `@ObservedObject`.** Use `@StateObject` when the view owns the lifecycle; `@ObservedObject` when the object is passed in from outside.
- **`MainActor` isolation.** Async functions that update `@Published` properties must run on `@MainActor`. Annotate the class or call `await MainActor.run { ... }`.
- **Force unwrapping (`!`).** SwiftLint `force_unwrapping` rule flags these. Use `guard let` or `if let` instead.
- **Previews use mock data.** `#Preview` blocks should never hit the network. Inject a `MockService` so previews work without credentials.
- **Deployment target.** Set `{{min_platform}}` in the project-level settings, not per-target, to stay consistent.
