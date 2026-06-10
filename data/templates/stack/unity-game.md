---
id = "unity-game"
version = "1.0.0"
tier = "any"
stacks = ["unity", "csharp"]
project_shapes = []
description = "Unity 6+ game project: C#, Assembly Definitions, NUnit, Scene/Prefab conventions"
---

# CASCADE Instructions — Unity Game

> Stack: Unity 6+ · C# · Assembly Definitions · NUnit · Scene/Prefab conventions
> Tier: any (typically PAC)

Use `{{project_name}}` for the Unity project name, `{{company_name}}` for the company name in Player Settings (e.g. `Acme`), and `{{namespace}}` for the root C# namespace (e.g. `Acme.{{project_name}}`).

---

## Module / Package Layout

Unity projects live under `Assets/`. Organise by feature, not by asset type.

```
{{project_name}}/
├── Assets/
│   ├── {{project_name}}/               # all project-specific assets
│   │   ├── Code/
│   │   │   ├── Runtime/                # runtime C# scripts
│   │   │   │   ├── Player/
│   │   │   │   ├── UI/
│   │   │   │   └── {{project_name}}.Runtime.asmdef
│   │   │   └── Editor/                 # editor-only scripts
│   │   │       └── {{project_name}}.Editor.asmdef
│   │   ├── Scenes/
│   │   │   ├── Main.unity
│   │   │   └── Bootstrap.unity
│   │   ├── Prefabs/
│   │   ├── Art/
│   │   │   ├── Textures/
│   │   │   └── Models/
│   │   └── Audio/
│   └── Tests/
│       ├── Runtime/
│       │   ├── PlayerTests.cs
│       │   └── {{project_name}}.Tests.Runtime.asmdef
│       └── Editor/
│           └── {{project_name}}.Tests.Editor.asmdef
├── Packages/
│   └── manifest.json
├── ProjectSettings/
└── .gitignore                          # exclude Library/, Temp/, Builds/
```

---

## Build & Tooling

Build via Unity Hub or the Unity CLI:

```bash
# Headless build (CI)
/Applications/Unity/Hub/Editor/<version>/Unity.app/Contents/MacOS/Unity \
  -quit -batchmode -nographics \
  -projectPath . \
  -buildTarget StandaloneOSX \
  -executeMethod BuildScript.BuildRelease \
  -logFile build.log

# Run edit-mode tests (CI)
Unity -quit -batchmode -nographics \
  -projectPath . \
  -runTests -testPlatform EditMode \
  -testResults test-results.xml
```

Keep a `BuildScript.cs` editor script in `Assets/{{project_name}}/Code/Editor/` for CI builds.

---

## Testing Convention

Unity uses NUnit via its built-in Test Runner. Two test modes:
- **Edit Mode** — runs in the Unity Editor without entering Play Mode; good for pure logic and editor utilities.
- **Play Mode** — runs inside the game loop; required for MonoBehaviour, physics, and scene interactions.

```csharp
// Assets/Tests/Runtime/PlayerTests.cs
using NUnit.Framework;
using {{namespace}}.Player;

public class PlayerHealthTests
{
    [Test]
    public void TakeDamage_ReducesHealth()
    {
        var health = new PlayerHealth(maxHp: 100);
        health.TakeDamage(25);
        Assert.AreEqual(75, health.Current);
    }

    [Test]
    public void TakeDamage_ClampsAtZero()
    {
        var health = new PlayerHealth(maxHp: 100);
        health.TakeDamage(200);
        Assert.AreEqual(0, health.Current);
    }
}
```

Test assemblies must reference the runtime assembly via their `.asmdef` file.

---

## Lint & Format

Unity does not ship a built-in linter. Use **Roslyn Analyzers** via a `Directory.Build.props` file and the `Microsoft.Unity.Analyzers` NuGet package. Enable nullable reference types in your `csc.rsp` or project settings.

Formatting: configure your IDE (Rider / VS / VSCode with OmniSharp) to use `allman` brace style and 4-space indentation, matching Unity's own source conventions.

CI: run the Unity Test Runner in headless mode and parse the JUnit XML output (`test-results.xml`).

---

## Assembly Definitions

Every feature area gets its own Assembly Definition (`.asmdef`) file. This enables:
- Faster incremental compilation (Unity only recompiles changed assemblies).
- Explicit dependency graph (no accidental cross-feature coupling).
- Editor-only code isolation.

**Rules:**
- Runtime assemblies must never reference `UnityEditor` namespace. The `.asmdef` `includePlatforms` must exclude `Editor`.
- Editor assemblies set `"includePlatforms": ["Editor"]` in their `.asmdef`.
- Test assemblies reference `UnityEngine.TestRunner` and `UnityEditor.TestRunner` and set `"optionalUnityReferences": ["TestAssemblies"]`.
- One `.asmdef` per top-level feature folder, not one per file.

---

## Scene Naming Convention

Scenes are the highest-level organisation unit. Follow these naming rules:

| Scene purpose | Name format | Example |
|---|---|---|
| Bootstrap / init | `Bootstrap` | `Bootstrap.unity` |
| Main menu | `MainMenu` | `MainMenu.unity` |
| Gameplay level | `Level_<number>` | `Level_01.unity` |
| Standalone feature | `<FeatureName>` | `Settings.unity` |

One scene per top-level feature. Use `Scenes/` as the scene folder. Never put gameplay Prefabs directly in a scene — reference them from a spawner or manager script so they can be iterated without opening the scene.

---

## Common Pitfalls

- **`Library/` in git.** Never commit the `Library/` or `Temp/` folders. They are generated per machine. The `.gitignore` must exclude them.
- **Meta files.** Every asset must have a `.meta` file committed alongside it. Missing meta files cause GUID mismatches and broken references for collaborators.
- **`Resources/` overuse.** `Resources.Load()` bypasses the asset bundle system and increases build size. Use `Addressables` or direct serialised references instead.
- **Null checks on `MonoBehaviour`.** Unity overloads `==` for `UnityEngine.Object`; `obj == null` is not the same as `obj is null`. Use `obj == null` for destroyed/missing objects, and `obj is null` for C# null checks on managed objects.
- **Assembly Definition mismatches.** If a script can't find a class that clearly exists, check whether the two scripts are in different assemblies that don't reference each other.
