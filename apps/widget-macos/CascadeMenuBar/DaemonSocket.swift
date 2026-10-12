import Foundation
import Network

enum DaemonError: String, Error, LocalizedError {
    case socketUnconfigured = "Daemon socket is not configured."
    case unauthorized = "Daemon access is unauthorized."
    case unavailable = "Daemon is unavailable."
    case invalidResponse = "Daemon returned an invalid response."
    case storageUnavailable = "Shared snapshot storage is unavailable."
    var errorDescription: String? { rawValue }
}

// Purpose: local HTTP over NWConnection's Unix endpoint, without browser headers.
// Inputs: CASCADE_SOCKET_PATH only, request bytes. Outputs: response body chunks on main queue.
// Constraints: bounded buffers, cancellation and timeout; no URLSession or alternate path discovery.
final class DaemonSocket {
    let path: String
    let connection: NWConnection
    private var buffer = Data()
    private var headerRead = false
    private var chunked = false
    private var remaining: Int?
    private var chunkRemaining: Int?
    private var finished = false
    private var onData: ((Data) -> Void)?
    private var onEnd: ((DaemonError?) -> Void)?
    private var deadline: DispatchWorkItem?
    private let limit = 4 * 1024 * 1024

    init(environment: [String: String] = ProcessInfo.processInfo.environment) throws {
        guard let value = environment["CASCADE_SOCKET_PATH"], value.hasPrefix("/"),
              !value.contains("\0"), value.utf8.count < 104 else { throw DaemonError.socketUnconfigured }
        path = value
        connection = NWConnection(to: .unix(path: value), using: .tcp)
    }

    static func requestBytes() -> Data {
        let body = #"{"jsonrpc":"2.0","id":1,"method":"status.widget"}"#
        return Data("POST /rpc HTTP/1.1\r\nHost: unix\r\nContent-Type: application/json\r\nContent-Length: \(body.utf8.count)\r\nConnection: close\r\n\r\n\(body)".utf8)
    }

    static func responseBody(_ data: Data) throws -> Data {
        guard let end = data.range(of: Data("\r\n\r\n".utf8)) else { throw DaemonError.invalidResponse }
        let header = String(decoding: data[..<end.lowerBound], as: UTF8.self)
        try validateStatus(header)
        return Data(data[end.upperBound...])
    }

    private static func validateStatus(_ header: String) throws {
        let fields = header.components(separatedBy: "\r\n")[0].split(separator: " ")
        guard fields.count >= 2 else { throw DaemonError.invalidResponse }
        if fields[1] == "403" { throw DaemonError.unauthorized }
        guard fields[0] == "HTTP/1.1", fields[1] == "200" else { throw DaemonError.invalidResponse }
    }

    func start(request: Data, streaming: Bool = false, onData: @escaping (Data) -> Void,
               onEnd: @escaping (DaemonError?) -> Void) {
        self.onData = onData
        self.onEnd = onEnd
        connection.stateUpdateHandler = { [weak self] state in
            guard let self, !self.finished else { return }
            switch state {
            case .ready:
                self.connection.send(content: request, completion: .contentProcessed { [weak self] error in
                    guard let self else { return }
                    if error != nil { self.end(.unavailable) } else { self.receive() }
                })
            case .failed, .waiting: self.end(.unavailable)
            default: break
            }
        }
        if !streaming {
            let deadline = DispatchWorkItem { [weak self] in self?.end(.unavailable) }
            self.deadline = deadline
            DispatchQueue.main.asyncAfter(deadline: .now() + 10, execute: deadline)
        }
        connection.start(queue: .main)
    }

    func cancel() { end(nil) }

    private func end(_ error: DaemonError?) {
        guard !finished else { return }
        finished = true
        deadline?.cancel()
        connection.stateUpdateHandler = nil
        connection.cancel()
        let callback = onEnd
        onData = nil
        onEnd = nil
        callback?(error)
    }

    private func receive() {
        connection.receive(minimumIncompleteLength: 1, maximumLength: 65_536) { [weak self] data, _, complete, error in
            guard let self, !self.finished else { return }
            do {
                if let data {
                    self.buffer.append(data)
                    guard self.buffer.count <= self.limit else { throw DaemonError.invalidResponse }
                    try self.drain()
                }
                if error != nil { self.end(.unavailable) }
                else if complete {
                    self.end(self.headerRead && !self.chunked && (self.remaining == nil || self.remaining == 0) ? nil : .invalidResponse)
                } else if !self.finished { self.receive() }
            } catch { self.end(error as? DaemonError ?? .invalidResponse) }
        }
    }

    private func drain() throws {
        if !headerRead {
            guard let boundary = buffer.range(of: Data("\r\n\r\n".utf8)) else { return }
            let header = String(decoding: buffer[..<boundary.lowerBound], as: UTF8.self)
            try Self.validateStatus(header)
            for line in header.components(separatedBy: "\r\n").dropFirst() {
                let pair = line.split(separator: ":", maxSplits: 1).map { $0.trimmingCharacters(in: .whitespaces).lowercased() }
                guard pair.count == 2 else { throw DaemonError.invalidResponse }
                if pair[0] == "transfer-encoding" { chunked = pair[1] == "chunked" }
                if pair[0] == "content-length" { remaining = Int(pair[1]) }
            }
            buffer.removeSubrange(..<boundary.upperBound)
            headerRead = true
        }
        if chunked { try drainChunks(); return }
        let count = min(buffer.count, remaining ?? buffer.count)
        if count > 0 {
            onData?(Data(buffer.prefix(count)))
            buffer.removeFirst(count)
            if let left = remaining { remaining = left - count }
        }
        if remaining == 0 { end(nil) }
    }

    private func drainChunks() throws {
        while !finished {
            if chunkRemaining == nil {
                guard let line = buffer.range(of: Data("\r\n".utf8)) else { return }
                let text = String(decoding: buffer[..<line.lowerBound], as: UTF8.self).split(separator: ";")[0]
                guard let count = Int(text, radix: 16), count >= 0, count <= limit else { throw DaemonError.invalidResponse }
                buffer.removeSubrange(..<line.upperBound)
                if count == 0 { end(nil); return }
                chunkRemaining = count
            }
            guard let count = chunkRemaining, buffer.count >= count + 2 else { return }
            let bytes = Array(buffer)
            guard bytes[count] == 13, bytes[count + 1] == 10 else { throw DaemonError.invalidResponse }
            onData?(Data(buffer.prefix(count)))
            buffer.removeFirst(count + 2)
            chunkRemaining = nil
        }
    }
}
