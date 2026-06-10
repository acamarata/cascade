---
id = "kotlin-app"
version = "1.0.0"
tier = "any"
stacks = ["kotlin", "android", "gradle"]
project_shapes = []
description = "Kotlin 2.x Android/JVM app: Gradle, JUnit 5, ktlint"
---

# CASCADE Instructions — Kotlin App

> Stack: Kotlin 2.x · Gradle (Kotlin DSL) · JUnit 5 · ktlint
> Tier: any (typically PAC)

Use `{{app_name}}` for the module/app name, `{{package_name}}` for the root package (e.g. `com.acme.myapp`), and `{{min_sdk}}` for the minimum Android SDK if targeting Android (e.g. `26`).

---

## Module / Package Layout

Multi-module Gradle project (single-module is fine for small apps — remove `:core` and `:feature` modules):

```
{{app_name}}/
├── app/
│   ├── src/
│   │   ├── main/
│   │   │   ├── kotlin/{{package_name | path}}/
│   │   │   │   ├── MainActivity.kt
│   │   │   │   └── AppApplication.kt
│   │   │   └── res/
│   │   └── test/
│   │       └── kotlin/{{package_name | path}}/
│   │           └── MainActivityTest.kt
│   └── build.gradle.kts
├── core/                        # shared business logic
│   ├── src/main/kotlin/
│   └── build.gradle.kts
├── gradle/
│   └── libs.versions.toml       # version catalog
├── build.gradle.kts             # root build file
├── settings.gradle.kts
├── .editorconfig
└── .editorconfig                # ktlint reads this
```

---

## Build & Tooling

Use Gradle Kotlin DSL (`.kts` files). Define all versions in `gradle/libs.versions.toml`.

**`gradle/libs.versions.toml` (excerpt):**

```toml
[versions]
kotlin = "2.0.0"
junit5 = "5.11.0"
ktlint = "12.1.1"

[libraries]
junit5-api     = { module = "org.junit.jupiter:junit-jupiter-api",    version.ref = "junit5" }
junit5-engine  = { module = "org.junit.jupiter:junit-jupiter-engine",  version.ref = "junit5" }
junit5-params  = { module = "org.junit.jupiter:junit-jupiter-params",  version.ref = "junit5" }

[plugins]
kotlin-jvm    = { id = "org.jetbrains.kotlin.jvm",    version.ref = "kotlin" }
ktlint        = { id = "org.jlleitschuh.gradle.ktlint", version.ref = "ktlint" }
```

**Common commands:**

```bash
# Build
./gradlew assembleDebug          # Android
./gradlew build                  # JVM

# Run tests
./gradlew test

# Lint (ktlint check)
./gradlew ktlintCheck

# Auto-format
./gradlew ktlintFormat
```

---

## Testing Convention

JUnit 5 with the Kotlin test helpers. Do not use JUnit 4 — it is the Gradle default but must be overridden.

```kotlin
// src/test/kotlin/{{package_name | path}}/UserServiceTest.kt
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.params.ParameterizedTest
import org.junit.jupiter.params.provider.ValueSource

class UserServiceTest {

    @Test
    fun `greeting returns expected message`() {
        val svc = UserService()
        assertEquals("Hello, Alice", svc.greet("Alice"))
    }

    @ParameterizedTest
    @ValueSource(strings = ["", "  "])
    fun `greet throws on blank name`(name: String) {
        val svc = UserService()
        assertThrows<IllegalArgumentException> { svc.greet(name) }
    }
}
```

Enable JUnit 5 in `build.gradle.kts`:

```kotlin
tasks.withType<Test> {
    useJUnitPlatform()
}
```

---

## Lint & Format

ktlint enforces code style. Apply via the Gradle plugin:

```kotlin
// root build.gradle.kts
plugins {
    alias(libs.plugins.ktlint)
}

ktlint {
    version.set("1.3.1")
    android.set(true)           // set false for pure JVM projects
    outputToConsole.set(true)
    reporters {
        reporter(org.jlleitschuh.gradle.ktlint.reporter.ReporterType.PLAIN)
    }
}
```

Pre-commit hook: run `./gradlew ktlintCheck` before pushing. CI fails on any ktlint violation.

---

## Common Pitfalls

- **JUnit 5 vs JUnit 4 coexistence.** Adding `junit:junit:4.x` as a dependency alongside JUnit 5 causes test runner confusion. Use only JUnit 5 (`org.junit.jupiter`); exclude `junit:junit` transitive dependencies.
- **`useJUnitPlatform()` is required.** Without it, Gradle does not discover JUnit 5 tests and reports zero tests run (which still shows as passing — a silent failure).
- **Kotlin DSL vs Groovy DSL.** Do not mix `.kts` and `.gradle` files in the same project. Stick to `.kts` throughout.
- **Version catalog over string literals.** Never hardcode version strings in `build.gradle.kts` files; always reference `libs.versions.toml` so versions are centralised.
- **`companion object` for constants.** Use `companion object { const val X = "x" }` instead of Java-style `static` fields.
