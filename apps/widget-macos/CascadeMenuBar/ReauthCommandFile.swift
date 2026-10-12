import Foundation

// Purpose: write the per-launch command file that Terminal runs for one confirmed reauthentication.
// Inputs: cache directory, UUID, bundled script path, installer-recorded CLI path, optional CASCADE_HOME, ref.
// Outputs: the file URL. Content is fixed literals plus single-quoted, validated values: the shebang,
// `rm -f "$0"`, an `export CASCADE_HOME='<abs>'` line only when a home is recorded, then `exec` of the
// bundled script with the CLI path and the ref.
// Constraints: directory 0700 and file 0700, created with O_EXCL | O_NOFOLLOW relative to the verified
// directory descriptor, so no other user and no pre-planted link can intercept it. A value with a quote or a
// control character, or a relative path, is refused before anything is written. The ref guard repeats the one
// in Resources/cascade-reauth.sh.
enum ReauthCommandFile {
    /// The interpreter named on the shebang line; the one place in Swift that spells it.
    static let interpreter = "/bin/sh"
    static let maxRefLength = 64

    /// True for 1-64 characters of A-Z a-z 0-9 . _ - with no leading dash.
    static func isValidRef(_ ref: String) -> Bool {
        let bytes = Array(ref.utf8)
        guard (1...maxRefLength).contains(bytes.count), bytes[0] != UInt8(ascii: "-") else { return false }
        return bytes.allSatisfy { byte in
            switch byte {
            case UInt8(ascii: "A")...UInt8(ascii: "Z"), UInt8(ascii: "a")...UInt8(ascii: "z"),
                 UInt8(ascii: "0")...UInt8(ascii: "9"), UInt8(ascii: "."), UInt8(ascii: "_"), UInt8(ascii: "-"):
                return true
            default:
                return false
            }
        }
    }

    /// True for an absolute path that is safe inside single quotes on one line.
    static func isQuotableAbsolutePath(_ path: String) -> Bool {
        guard path.hasPrefix("/"), !path.contains("'") else { return false }
        return !path.unicodeScalars.contains { $0.value < 0x20 || $0.value == 0x7f }
    }

    /// The file content: three lines, four when a home is recorded. Validates every interpolated value.
    static func render(script: String, cli: String, home: String?, ref: String) throws -> String {
        guard isValidRef(ref) else { throw LaunchError.invalidProviderRef }
        guard isQuotableAbsolutePath(script) else { throw LaunchError.invalidBundlePath }
        guard isQuotableAbsolutePath(cli) else { throw LaunchError.cliUnavailable }
        var lines = ["#!" + interpreter, "rm -f \"$0\""]
        if let home {
            guard isQuotableAbsolutePath(home) else { throw LaunchError.cliUnavailable }
            lines.append("export CASCADE_HOME='\(home)'")
        }
        lines.append("exec '\(script)' '\(cli)' '\(ref)'")
        return lines.joined(separator: "\n") + "\n"
    }

    /// Writes `reauth-<UUID>.command` into `directory` (created 0700 when missing) and returns its URL.
    static func create(in directory: URL, id: UUID = UUID(), script: String, cli: String, home: String?,
                       ref: String) throws -> URL {
        let content = try render(script: script, cli: cli, home: home, ref: ref)
        let name = "reauth-\(id.uuidString).command"
        guard directory.isFileURL else { throw LaunchError.commandFileUnwritable }
        _ = mkdir(directory.path, 0o700)
        let dirFD = open(directory.path, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC)
        guard dirFD >= 0 else { throw LaunchError.commandFileUnwritable }
        defer { close(dirFD) }
        var info = stat()
        guard fstat(dirFD, &info) == 0, info.st_uid == geteuid() else { throw LaunchError.commandFileUnwritable }
        if info.st_mode & 0o777 != 0o700, fchmod(dirFD, 0o700) != 0 { throw LaunchError.commandFileUnwritable }
        let fileFD = openat(dirFD, name, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0o700)
        guard fileFD >= 0 else { throw LaunchError.commandFileUnwritable }
        defer { close(fileFD) }
        do {
            guard fchmod(fileFD, 0o700) == 0 else { throw LaunchError.commandFileUnwritable }
            try write(Array(content.utf8), to: fileFD)
        } catch {
            unlinkat(dirFD, name, 0)
            throw error
        }
        return directory.appendingPathComponent(name)
    }

    private static func write(_ bytes: [UInt8], to descriptor: Int32) throws {
        var offset = 0
        while offset < bytes.count {
            let written = bytes[offset...].withUnsafeBytes { Darwin.write(descriptor, $0.baseAddress, $0.count) }
            if written < 0, errno == EINTR { continue }
            guard written > 0 else { throw LaunchError.commandFileUnwritable }
            offset += written
        }
    }
}
