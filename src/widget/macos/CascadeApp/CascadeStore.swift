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

    private var primaryURL: URL {
        FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".cascade/accounts/quota.json")
    }
    private var fallbackURL: URL {
        FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".claude/usage-cache.json")
    }

    /// Watch a file for changes and refresh immediately. Cascade writes these files
    /// ATOMICALLY (tmp + rename), which deletes the watched inode — so on a delete/rename
    /// we re-establish the watch on the new file. Without this the kqueue dies after the
    /// first atomic write and the widget only updates on the slow timer. `rearm` rebuilds
    /// the source so updates land instantly (e.g. right after a re-auth re-poll).
    private func watchFile(at url: URL, rearm: @escaping () -> Void) -> DispatchSourceFileSystemObject? {
        let fd = open(url.path, O_EVTONLY)
        guard fd >= 0 else {
            // File not present yet — retry shortly so a freshly-created cache gets watched.
            DispatchQueue.main.asyncAfter(deadline: .now() + 1.0) { rearm() }
            return nil
        }
        let src = DispatchSource.makeFileSystemObjectSource(
            fileDescriptor: fd,
            eventMask: [.write, .extend, .rename, .delete],
            queue: .main
        )
        src.setEventHandler { [weak self] in
            self?.refresh()
            let flags = src.data
            if flags.contains(.delete) || flags.contains(.rename) {
                src.cancel()
                DispatchQueue.main.asyncAfter(deadline: .now() + 0.05) { rearm() }
            }
        }
        src.setCancelHandler { close(fd) }
        src.resume()
        return src
    }

    private func startFileWatchers() {
        armPrimary()
        armFallback()
    }

    private func armPrimary() {
        primarySource = watchFile(at: primaryURL) { [weak self] in self?.armPrimary() }
    }
    private func armFallback() {
        fallbackSource = watchFile(at: fallbackURL) { [weak self] in self?.armFallback() }
    }

    // MARK: - Poll timer

    /// Short fallback poll (5s) in case a file event is ever missed — cheap (one stat +
    /// small JSON decode) and keeps the live "Ns ago" counter honest.
    private func scheduleTimer() {
        timer = Timer.scheduledTimer(withTimeInterval: 5, repeats: true) { [weak self] _ in
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
