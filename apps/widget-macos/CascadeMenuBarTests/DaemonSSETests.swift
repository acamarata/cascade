import XCTest
import Network
@testable import CascadeMenuBar

final class DaemonSSETests: XCTestCase {
    func testReconnectSendsLastEventIDAfterDrop() throws {
        let path = "/tmp/sse-" + String(UUID().uuidString.prefix(8))
        let parameters = NWParameters.tcp
        parameters.requiredLocalEndpoint = .unix(path: path)
        let listener = try NWListener(using: parameters)
        defer { listener.cancel(); try? FileManager.default.removeItem(atPath: path) }
        let sse = DaemonSSE(environment: ["CASCADE_SOCKET_PATH": path])
        defer { sse.stop() }
        let resumed = expectation(description: "reconnect request carries the received event cursor")
        let fixture = try WidgetModelsTests.fixture("fixture_status_widget_changed_sse", ext: "txt")
        var connections = 0
        listener.newConnectionHandler = { connection in
            connections += 1
            let attempt = connections
            connection.start(queue: .main)
            connection.receive(minimumIncompleteLength: 1, maximumLength: 4096) { bytes, _, _, error in
                XCTAssertNil(error)
                let request = String(decoding: bytes ?? Data(), as: UTF8.self)
                if attempt == 1 {
                    let reply = Data("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nConnection: close\r\n\r\n".utf8) + fixture
                    connection.send(content: reply, completion: .contentProcessed { _ in connection.cancel() })
                } else {
                    XCTAssertTrue(request.contains("Last-Event-ID: AAAAAAAAAAE\r\n"))
                    resumed.fulfill()
                    sse.stop()
                    connection.cancel()
                }
            }
        }
        listener.stateUpdateHandler = { state in
            if case .ready = state { sse.start() }
            if case .failed(let error) = state { XCTFail("Listener failed: \(error)") }
        }
        listener.start(queue: .main)
        wait(for: [resumed], timeout: 5)
        XCTAssertEqual(connections, 2)
    }

    func testFixtureOtherKindsMalformedAndResume() throws {
        let sse = DaemonSSE(environment: ["CASCADE_SOCKET_PATH": "/tmp/widget-fixture.sock"])
        let bytes = try WidgetModelsTests.fixture("fixture_status_widget_changed_sse", ext: "txt")
        var snapshots: [Data] = []
        sse.onSnapshot = { snapshots.append($0) }
        let valid = try ResultSlicer.result(in: WidgetModelsTests.fixture())
        XCTAssertNotNil(try? WidgetSnapshot.decode(valid))
        let other = "data: {\"kind\":\"other\",\"payload\":\"\(valid.base64EncodedString())\"}\n\n"
        sse.consume(Data(other.utf8))
        XCTAssertTrue(snapshots.isEmpty, "A non-matching kind must not deliver a valid snapshot")
        for byte in bytes { sse.consume(Data([byte])) }
        XCTAssertEqual(snapshots.count, 1)
        XCTAssertEqual(try WidgetSnapshot.decode(XCTUnwrap(snapshots.first)).rows.count, 3)
        XCTAssertEqual(sse.lastEventID, "AAAAAAAAAAE")
        let request = String(decoding: sse.requestBytes(), as: UTF8.self)
        XCTAssertTrue(request.contains("Last-Event-ID: AAAAAAAAAAE\r\n"))
        XCTAssertFalse(request.contains("Origin:"))
        XCTAssertTrue(request.contains("Cascade-Client: owner-cli\r\n"))
        let previous = snapshots
        sse.consume(Data("id: bad\ndata: {broken}\n\n".utf8))
        XCTAssertEqual(snapshots, previous)
        XCTAssertEqual(sse.lastEventID, "AAAAAAAAAAE")
    }
}
