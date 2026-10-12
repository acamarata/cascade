import Foundation
import WidgetKit

// Purpose: publish received snapshot bytes to the app's shared group container.
// Inputs: validated snapshot bytes and a timeline reloader. Outputs: atomic protected file, then one reload.
// Constraints: no re-encoding or extra keys; a failed write never reloads the widget timelines.
struct SharedContainer {
    let directory: URL
    var reloadTimelines: () -> Void = { WidgetCenter.shared.reloadAllTimelines() }
    var file: URL { directory.appendingPathComponent("widget-snapshot.json") }

    static func application() throws -> SharedContainer {
        guard let group = Bundle.main.object(forInfoDictionaryKey: "CascadeAppGroup") as? String,
              !group.isEmpty, !group.contains("$("),
              let directory = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: group) else {
            throw DaemonError.storageUnavailable
        }
        return SharedContainer(directory: directory)
    }

    func write(_ bytes: Data) throws {
        do { try writeProtected(bytes) }
        catch {
            try invalidate()
            throw error
        }
        reloadTimelines()
    }

    // Remove failed publications; a non-writable directory may still allow truncating its file.
    func invalidate() throws {
        let manager = FileManager.default
        guard manager.fileExists(atPath: file.path) else { return }
        do { try manager.removeItem(at: file) }
        catch {
            let handle = try FileHandle(forWritingTo: file)
            defer { try? handle.close() }
            try handle.truncate(atOffset: 0)
            try handle.synchronize()
        }
    }

    private func writeProtected(_ bytes: Data) throws {
        let manager = FileManager.default
        try manager.createDirectory(at: directory, withIntermediateDirectories: true)
        let temporary = directory.appendingPathComponent("." + UUID().uuidString)
        defer { try? manager.removeItem(at: temporary) }
        guard manager.createFile(atPath: temporary.path, contents: nil,
                                 attributes: [.posixPermissions: 0o600, .protectionKey: FileProtectionType.complete]) else {
            throw DaemonError.storageUnavailable
        }
        try manager.setAttributes([.protectionKey: FileProtectionType.complete], ofItemAtPath: temporary.path)
        let attributes = try manager.attributesOfItem(atPath: temporary.path)
        guard attributes[.protectionKey] as? FileProtectionType == .complete else { throw DaemonError.storageUnavailable }
        let handle = try FileHandle(forWritingTo: temporary)
        defer { try? handle.close() }
        try handle.write(contentsOf: bytes)
        try handle.synchronize()
        try handle.close()
        guard rename(temporary.path, file.path) == 0 else { throw DaemonError.storageUnavailable }
    }
}
