import Foundation

// MARK: - DaemonStatus
//
// Purpose: Codable mirror of the JSON-RPC 2.0 `cascade.status` result payload
//          returned by the `cascaded` daemon over the Unix domain socket.
//          All fields are optional — the daemon may omit any field without
//          breaking decoding here.
// Inputs:  JSONDecoder from raw socket bytes.
// Outputs: Struct consumed by MenubarController.updateIcon() to drive icon state.
// Constraints: Field names match the E-03 IPC JSON-RPC contract exactly.
// SPORT: .opencode/phases/sport/ — DaemonStatus model (P2, T-P2-E04-10)

struct DaemonStatus: Codable {
    var pid: Int?
    var uptime_seconds: Int?
    var version: String?
    var queue_depth: Int?
    var index_freshness_seconds: Int?
    var status: String?
}

// MARK: - DaemonIPCClient
//
// Purpose: Connect to the running `cascaded` daemon over a POSIX AF_UNIX stream
//          socket, send a JSON-RPC 2.0 `cascade.status` request, and decode the
//          response into a DaemonStatus value.
// Inputs:  None — socket path is derived from the user's home directory.
// Outputs: DaemonStatus? via async completion; nil when the socket is absent or
//          when any POSIX call fails (daemon not running is the normal case).
// Constraints:
//   - All socket I/O is dispatched on a background queue (never blocks main thread).
//   - Completion is always called on the main queue.
//   - No retry / reconnect — a nil result is the correct "offline" indicator.
//   - defer { close(fd) } ensures the file descriptor is released even on early
//     returns; guard branches before defer are safe (fd is -1 before assignment).
//   - CascadeApp is NOT sandboxed (see CascadeApp.entitlements: no App Sandbox
//     entitlement). AF_UNIX to ~/.cascade/cascaded.sock is therefore allowed.
//     WidgetExtension IS sandboxed — DaemonIPCClient must NOT be used there.
// SPORT: .opencode/phases/sport/ — DaemonIPCClient component (P2, T-P2-E04-10)

class DaemonIPCClient {
    /// Absolute path to the cascaded Unix domain socket.
    static let socketPath: String = FileManager.default.homeDirectoryForCurrentUser
        .appendingPathComponent(".cascade/cascaded.sock").path

    /// Query the daemon's status endpoint asynchronously.
    ///
    /// - Parameter completion: Called on the main queue with the decoded status,
    ///   or nil when the daemon is not reachable.
    func queryStatus(completion: @escaping (DaemonStatus?) -> Void) {
        DispatchQueue.global(qos: .background).async {
            let result = Self.performQuery()
            DispatchQueue.main.async { completion(result) }
        }
    }

    /// Synchronous POSIX implementation — called only from the background queue.
    private static func performQuery() -> DaemonStatus? {
        // Create the socket file descriptor.
        let fd = Darwin.socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { return nil }
        defer { Darwin.close(fd) }

        // Build the sockaddr_un struct with the socket path.
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let path = socketPath
        // strlcpy copies at most `size` bytes including NUL terminator.
        // MemoryLayout.size(ofValue: addr.sun_path) gives the full array size.
        // Capture the sun_path buffer size before taking a pointer into addr,
        // avoiding the Swift exclusive-access overlap on addr.sun_path.0.
        let sunPathSize = MemoryLayout.size(ofValue: addr.sun_path)
        _ = path.withCString { cStr in
            withUnsafeMutablePointer(to: &addr.sun_path.0) { dst in
                strlcpy(dst, cStr, sunPathSize)
            }
        }
        let addrLen = socklen_t(MemoryLayout<sockaddr_un>.size)

        // Connect — returns 0 on success, -1 if socket file is absent (ENOENT)
        // or daemon is not listening (ECONNREFUSED). Both map to nil (offline).
        let connectResult = withUnsafePointer(to: &addr) { addrPtr in
            addrPtr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sockAddrPtr in
                Darwin.connect(fd, sockAddrPtr, addrLen)
            }
        }
        guard connectResult == 0 else { return nil }

        // Send the JSON-RPC 2.0 cascade.status request followed by a newline
        // as the message delimiter expected by the daemon's line-framed parser.
        let request = "{\"jsonrpc\":\"2.0\",\"method\":\"cascade.status\",\"id\":1}\n"
        let bytesWritten = request.withCString { ptr in
            Darwin.write(fd, ptr, strlen(ptr))
        }
        guard bytesWritten > 0 else { return nil }

        // Read the response into a fixed 4 KiB buffer — sufficient for the
        // small status payload. A single read is used; the daemon closes the
        // connection after responding, so blocking here is safe.
        var buf = [CChar](repeating: 0, count: 4096)
        let bytesRead = Darwin.read(fd, &buf, 4095)
        guard bytesRead > 0 else { return nil }

        // Decode the JSON-RPC 2.0 response envelope to extract `result`.
        let responseData = Data(bytes: buf, count: bytesRead)
        struct RPCResponse: Codable {
            var result: DaemonStatus?
        }
        guard let decoded = try? JSONDecoder().decode(RPCResponse.self, from: responseData) else {
            return nil
        }
        return decoded.result
    }
}
