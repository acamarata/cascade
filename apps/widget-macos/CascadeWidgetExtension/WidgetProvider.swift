import Foundation
import WidgetKit

// Purpose: one timeline entry; a nil snapshot is the no-data state rendered with the em-dash.
// Inputs: entry date and the snapshot decoded from the App Group file. Outputs: a WidgetKit entry.
struct CascadeEntry: TimelineEntry {
    let date: Date
    let snapshot: WidgetSnapshot?
}

// Purpose: read the snapshot file the menu-bar app publishes, and nothing else.
// Inputs: the file URL. Outputs: a decoded snapshot, or nil for a missing, empty, oversized or corrupt file.
// Constraints: no network, no cache; every call reads the file again.
enum SnapshotFeed {
    static let fileName = "widget-snapshot.json"
    static let maxBytes = 1 << 20

    static func applicationFile() -> URL? {
        guard let group = Bundle.main.object(forInfoDictionaryKey: "CascadeAppGroup") as? String,
              !group.isEmpty, !group.contains("$("),
              let directory = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: group) else {
            return nil
        }
        return directory.appendingPathComponent(fileName)
    }

    static func read(_ file: URL?) -> WidgetSnapshot? {
        guard let file, let handle = try? FileHandle(forReadingFrom: file) else { return nil }
        defer { try? handle.close() }
        guard let data = try? handle.read(upToCount: maxBytes + 1),
              !data.isEmpty, data.count <= maxBytes else { return nil }
        return try? WidgetSnapshot.decode(data)
    }
}

// Purpose: build WidgetKit timelines from the injected App Group file on a 30 s refresh policy.
// Inputs: file resolver and clock. Outputs: single-entry timelines; placeholders carry no data.
struct CascadeProvider: TimelineProvider {
    static let refreshInterval: TimeInterval = 30
    let file: () -> URL?
    let now: () -> Date

    init(file: @escaping () -> URL? = SnapshotFeed.applicationFile, now: @escaping () -> Date = Date.init) {
        self.file = file
        self.now = now
    }

    func entry() -> CascadeEntry {
        CascadeEntry(date: now(), snapshot: SnapshotFeed.read(file()))
    }

    func timeline() -> Timeline<CascadeEntry> {
        let current = entry()
        return Timeline(entries: [current], policy: .after(current.date.addingTimeInterval(Self.refreshInterval)))
    }

    func placeholder(in context: Context) -> CascadeEntry {
        CascadeEntry(date: now(), snapshot: nil)
    }

    func getSnapshot(in context: Context, completion: @escaping (CascadeEntry) -> Void) {
        completion(entry())
    }

    func getTimeline(in context: Context, completion: @escaping (Timeline<CascadeEntry>) -> Void) {
        completion(timeline())
    }
}
