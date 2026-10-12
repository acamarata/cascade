import Foundation

// Purpose: own RPC, SSE, polling and raw snapshot publication for the menu bar.
// Inputs: environment and injected storage/interval. Outputs: main-queue view updates.
// start() resolves storage first so a bad socket still invalidates a previous run's snapshot.
final class DaemonClient {
    private let environment: [String: String]
    private var shared: SharedContainer?
    private let interval: TimeInterval
    private let resolveShared: () throws -> SharedContainer
    private var rpc: DaemonSocket?
    private var running = false
    private(set) var snapshot: WidgetSnapshot?
    private(set) var error: DaemonError?
    private(set) var lastSource: String?
    var onChange: (() -> Void)?
    let sse: DaemonSSE
    private lazy var poll = DaemonPollFallback(interval: interval) { [weak self] in self?.refresh() }

    init(environment: [String: String] = ProcessInfo.processInfo.environment,
         shared: SharedContainer? = nil, pollInterval: TimeInterval = DaemonPollFallback.productionInterval,
         resolveShared: @escaping () throws -> SharedContainer = SharedContainer.application) {
        self.environment = environment
        self.resolveShared = resolveShared
        self.shared = shared
        interval = pollInterval
        sse = DaemonSSE(environment: environment)
    }

    func start() {
        guard !running else { return }
        if shared == nil { shared = try? resolveShared() }
        do {
            _ = try DaemonSocket(environment: environment)
            guard shared != nil else { throw DaemonError.storageUnavailable }
        } catch { fail(error as? DaemonError ?? .storageUnavailable); return }
        running = true
        sse.onSnapshot = { [weak self] bytes in
            self?.poll.receivedEvent()
            self?.accept(bytes, source: "sse")
        }
        sse.onError = { [weak self] in self?.fail($0) }
        sse.start()
        refresh()
        poll.start()
    }

    func stop() {
        running = false
        poll.stop()
        sse.stop()
        rpc?.cancel()
        rpc = nil
    }

    private func refresh() {
        guard running, rpc == nil else { return }
        do {
            let socket = try DaemonSocket(environment: environment)
            rpc = socket
            var body = Data()
            socket.start(request: DaemonSocket.requestBytes(), onData: { bytes in body.append(bytes) }, onEnd: { [weak self] error in
                guard let self else { return }
                self.rpc = nil
                guard self.running else { return }
                if let error { self.fail(error); return }
                do { self.accept(try ResultSlicer.result(in: body), source: "rpc") }
                catch { self.fail(.invalidResponse) }
            })
        } catch { fail(error as? DaemonError ?? .unavailable) }
    }

    private func accept(_ bytes: Data, source: String) {
        do {
            let snapshot = try WidgetSnapshot.decode(bytes)
            guard let shared else { throw DaemonError.storageUnavailable }
            do { try shared.write(bytes) }
            catch { throw DaemonError.storageUnavailable }
            self.snapshot = snapshot
            lastSource = source
            error = nil
            onChange?()
        } catch { fail(error as? DaemonError ?? .invalidResponse) }
    }

    private func fail(_ error: DaemonError) {
        snapshot = nil
        lastSource = nil
        try? shared?.invalidate()
        self.error = error
        onChange?()
    }
}
