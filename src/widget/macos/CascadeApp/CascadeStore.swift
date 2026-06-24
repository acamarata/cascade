import SwiftUI
import Combine

// MARK: - CascadeStore
// Ported from ClawFleet CacheStore.swift, renamed to CascadeStore.
// Watches BOTH data paths for changes via DispatchSource file descriptors:
//   primary:  ~/.cascade/accounts/quota.json
//   fallback: ~/.claude/usage-cache.json
// Preference: primary when it has accounts, else fallback (see loadUsageCache()).
// Poll loop: 60s (underlying refresh daemons run every 5 min; 60s is sufficient).

@MainActor
final class CascadeStore: ObservableObject {
    @Published private(set) var cache: UsageCache?
    @Published private(set) var loadError: String?
    @Published private(set) var lastLoadedAt: Date?

    private var timer: Timer?
    // File-watch sources for both cache paths.
    private var primarySource:  DispatchSourceFileSystemObject?
    private var fallbackSource: DispatchSourceFileSystemObject?

    init() {
        refresh()
        scheduleTimer()
        startFileWatchers()
    }

    deinit {
        timer?.invalidate()
        primarySource?.cancel()
        fallbackSource?.cancel()
    }

    func refresh() {
        switch loadUsageCache() {
        case .success(let c):
            cache = c
            loadError = nil
            lastLoadedAt = Date()
        case .failure(let err):
            switch err {
            case .notFound:
                loadError = "No cache found. Run cascade or claw-fleet-refresh."
            case .decodeFailure(let underlying):
                loadError = "JSON parse error: \(underlying.localizedDescription)"
            }
            lastLoadedAt = Date()
        }
    }

    // MARK: - File watchers

    /// Watch a file path for write/rename events and trigger refresh.
    /// Uses kqueue via DispatchSource — no polling overhead.
    private func watchFile(at url: URL) -> DispatchSourceFileSystemObject? {
        let path = url.path
        let fd = open(path, O_EVTONLY)
        guard fd >= 0 else { return nil }
        let src = DispatchSource.makeFileSystemObjectSource(
            fileDescriptor: fd,
            eventMask: [.write, .rename, .delete],
            queue: .main
        )
        src.setEventHandler { [weak self] in
            self?.refresh()
        }
        src.setCancelHandler { close(fd) }
        src.resume()
        return src
    }

    private func startFileWatchers() {
        let home = FileManager.default.homeDirectoryForCurrentUser
        let primaryURL  = home.appendingPathComponent(".cascade/accounts/quota.json")
        let fallbackURL = home.appendingPathComponent(".claude/usage-cache.json")
        primarySource  = watchFile(at: primaryURL)
        fallbackSource = watchFile(at: fallbackURL)
    }

    // MARK: - Poll timer

    private func scheduleTimer() {
        timer = Timer.scheduledTimer(withTimeInterval: 60, repeats: true) { [weak self] _ in
            Task { @MainActor [weak self] in
                self?.refresh()
            }
        }
    }
}

// MARK: - Menu bar summary

extension CascadeStore {
    /// Compact label for the menu-bar icon: "X/N" (accounts with headroom / total).
    /// Falls back to "CS" before the cache loads.
    var summaryTitle: String {
        guard let accounts = cache?.accounts, !accounts.isEmpty else {
            return "CS"
        }
        let total = accounts.count
        let available = accounts.filter { entry in
            guard entry.quota_opaque != true else { return false }
            let weekPct = entry.usage?.seven_day?.utilization ?? 0
            let sessPct = entry.usage?.five_hour?.utilization ?? 0
            return weekPct < 100 && sessPct < 100
        }.count
        return "\(available)/\(total)"
    }
}
