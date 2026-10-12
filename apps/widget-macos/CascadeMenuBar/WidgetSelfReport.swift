import Foundation

// Purpose: write the self-report file `cascade doctor --widget` reads (contract widget-self-report).
// Inputs: CASCADE_WIDGET_STATUS_PATH from the injected environment, an injected clock.
// Outputs: an atomic 0600 JSON file with exactly bundle_path, window_level, written_at, first_rows_at.
// Constraints: the path comes only from the environment; it is never derived from a home directory.
final class WidgetSelfReport {
    static let pathKey = "CASCADE_WIDGET_STATUS_PATH"
    static let unsetMessage = "Self-report is off: run `cascade widget install`."

    let file: URL?
    private let now: () -> Date
    private var bundlePath: String?
    private var windowLevel: Int?
    private(set) var firstRowsAt: Date?
    private(set) var lastError: Error?

    init(environment: [String: String], now: @escaping () -> Date = Date.init) {
        let value = environment[Self.pathKey] ?? ""
        file = value.hasPrefix("/") ? URL(fileURLWithPath: value) : nil
        self.now = now
    }

    /// The one popover line shown when no self-report path is configured; nil when configured.
    var statusLine: String? { file == nil ? Self.unsetMessage : nil }

    /// Records the launch state (bundle path and panel level) and writes the file.
    func launched(bundlePath: String, windowLevel: Int) {
        self.bundlePath = bundlePath
        self.windowLevel = windowLevel
        write()
    }

    /// Records a panel level change; writes only when the level differs.
    func levelChanged(_ level: Int) {
        guard bundlePath != nil, level != windowLevel else { return }
        windowLevel = level
        write()
    }

    /// Records a status.widget answer; the first answer with a row fixes first_rows_at and writes once.
    func answered(rowCount: Int) {
        guard bundlePath != nil, rowCount > 0, firstRowsAt == nil else { return }
        firstRowsAt = now()
        write()
    }

    private func write() {
        guard let file, let bundlePath, let windowLevel else { return }
        let body: [String: Any] = [
            "bundle_path": bundlePath,
            "window_level": windowLevel,
            "written_at": Self.rfc3339(now()),
            "first_rows_at": firstRowsAt.map(Self.rfc3339) ?? NSNull()
        ]
        do {
            let bytes = try JSONSerialization.data(withJSONObject: body, options: [.sortedKeys])
            try Self.replace(file, with: bytes)
            lastError = nil
        } catch { lastError = error }
    }

    static func rfc3339(_ date: Date) -> String {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.string(from: date)
    }

    // Writes a 0600 temporary file beside the target, then renames it over the target.
    private static func replace(_ file: URL, with bytes: Data) throws {
        let manager = FileManager.default
        let parent = file.deletingLastPathComponent()
        try manager.createDirectory(at: parent, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        try manager.setAttributes([.posixPermissions: 0o700], ofItemAtPath: parent.path)
        let temporary = parent.appendingPathComponent("." + file.lastPathComponent + "." + UUID().uuidString)
        defer { try? manager.removeItem(at: temporary) }
        guard manager.createFile(atPath: temporary.path, contents: nil, attributes: [.posixPermissions: 0o600]) else {
            throw CocoaError(.fileWriteUnknown)
        }
        let handle = try FileHandle(forWritingTo: temporary)
        defer { try? handle.close() }
        try handle.write(contentsOf: bytes)
        try handle.synchronize()
        guard rename(temporary.path, file.path) == 0 else { throw CocoaError(.fileWriteUnknown) }
    }
}
