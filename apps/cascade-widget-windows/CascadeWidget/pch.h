// pch.h — Cascade Widget Provider
// Purpose:  Precompiled header. Includes stable Windows and C++/WinRT headers
//           that change rarely so that incremental build times are minimised.
// Constraints:
//   - Only include headers that are truly stable and shared across most TUs.
//   - Do NOT include generated cppwinrt projection headers here — those change
//     during codegen and would invalidate the PCH on every IDL edit.
// SPORT: cascade/apps/cascade-widget-windows

#pragma once

// ── Windows SDK ──────────────────────────────────────────────────────────────
#ifndef WIN32_LEAN_AND_MEAN
#define WIN32_LEAN_AND_MEAN
#endif
#ifndef NOMINMAX
#define NOMINMAX
#endif
#include <windows.h>
#include <unknwn.h>
#include <restrictederrorinfo.h>
#include <hstring.h>

// ── C++/WinRT projection infrastructure ─────────────────────────────────────
// winrt/base.h provides coroutine support, IUnknown projection, hstring, etc.
// Do NOT include specific winrt/*.h headers in the PCH — they pull in generated
// code and will vary between cppwinrt codegen runs.
#include <winrt/base.h>

// ── Windows App SDK 1.5 widget provider headers ──────────────────────────────
// These are stable SDK headers; the generated projection code is in IntDir.
#include <winrt/Microsoft.Windows.Widgets.Providers.h>

// ── Windows Implementation Library (WIL) ────────────────────────────────────
#include <wil/cppwinrt.h>
#include <wil/resource.h>
#include <wil/result.h>
#include <wil/com.h>

// ── STL ──────────────────────────────────────────────────────────────────────
#include <string>
#include <string_view>
#include <memory>
#include <vector>
#include <unordered_map>
#include <mutex>
#include <atomic>
#include <functional>
#include <optional>
#include <cassert>
