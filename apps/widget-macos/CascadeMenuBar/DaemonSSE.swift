import Foundation

// Purpose: follow widget events and resume after drops. All state belongs to the main queue.
// Inputs: chunk-decoded SSE bytes. Outputs: validated, base64-decoded snapshots, unchanged.
final class DaemonSSE {
    private let environment: [String: String]
    private var buffer = Data()
    private var socket: DaemonSocket?
    private var reconnect: DispatchWorkItem?
    private var running = false
    private(set) var lastEventID: String?
    var onSnapshot: ((Data) -> Void)?
    var onError: ((DaemonError) -> Void)?

    init(environment: [String: String]) { self.environment = environment }

    func requestBytes() -> Data {
        var request = "GET /events?filter=status.widget_changed HTTP/1.1\r\nHost: unix\r\nCascade-Client: owner-cli\r\nAccept: text/event-stream\r\n"
        if let lastEventID { request += "Last-Event-ID: \(lastEventID)\r\n" }
        return Data((request + "\r\n").utf8)
    }

    func start() { running = true; connect() }

    func stop() {
        running = false
        reconnect?.cancel()
        reconnect = nil
        socket?.cancel()
        socket = nil
        buffer.removeAll()
    }

    private func connect() {
        guard running else { return }
        do {
            let connection = try DaemonSocket(environment: environment)
            socket = connection
            connection.start(request: requestBytes(), streaming: true, onData: { [weak self] in self?.consume($0) },
                             onEnd: { [weak self] error in self?.dropped(error) })
        } catch { onError?(error as? DaemonError ?? .unavailable) }
    }

    private func dropped(_ error: DaemonError?) {
        guard running else { return }
        if let error { onError?(error) }
        buffer.removeAll()
        let work = DispatchWorkItem { [weak self] in self?.connect() }
        reconnect = work
        DispatchQueue.main.asyncAfter(deadline: .now() + 1, execute: work)
    }

    func consume(_ bytes: Data) {
        buffer.append(bytes)
        guard buffer.count <= 4 * 1024 * 1024 else { buffer.removeAll(); return }
        while let end = buffer.range(of: Data("\n\n".utf8)) {
            let frame = String(decoding: buffer[..<end.lowerBound], as: UTF8.self)
            buffer.removeSubrange(..<end.upperBound)
            accept(frame)
        }
    }

    private func accept(_ frame: String) {
        let lines = frame.components(separatedBy: "\n")
        let json = lines.filter { $0.hasPrefix("data:") }.map { String($0.dropFirst(5)).trimmingCharacters(in: .whitespaces) }.joined(separator: "\n")
        guard let envelope = try? JSONSerialization.jsonObject(with: Data(json.utf8)) as? [String: Any],
              envelope["kind"] as? String == "status.widget_changed",
              let payload = envelope["payload"] as? String, let bytes = Data(base64Encoded: payload),
              (try? WidgetSnapshot.decode(bytes)) != nil else { return }
        if let id = lines.first(where: { $0.hasPrefix("id:") }) {
            let value = String(id.dropFirst(3)).trimmingCharacters(in: .whitespaces)
            guard !value.isEmpty, value.rangeOfCharacter(from: .controlCharacters) == nil else { return }
            lastEventID = value
        }
        onSnapshot?(bytes)
    }
}
