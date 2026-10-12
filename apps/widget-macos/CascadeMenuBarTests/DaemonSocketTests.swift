import XCTest
@testable import CascadeMenuBar

final class DaemonSocketTests: XCTestCase {
    func testRequestBytesPassLocalGuard() {
        let body = #"{"jsonrpc":"2.0","id":1,"method":"status.widget"}"#
        let expected = "POST /rpc HTTP/1.1\r\nHost: unix\r\nCascade-Client: owner-cli\r\nContent-Type: application/json\r\nContent-Length: \(body.utf8.count)\r\nConnection: close\r\n\r\n\(body)"
        XCTAssertEqual(DaemonSocket.requestBytes(), Data(expected.utf8))
    }

    func testForbiddenIsUnauthorized() {
        XCTAssertThrowsError(try DaemonSocket.responseBody(Data("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n".utf8))) {
            XCTAssertEqual($0 as? DaemonError, .unauthorized)
        }
    }

    func testSocketPathFromEnvOnly() throws {
        for env in [[:], ["CASCADE_SOCKET_PATH": "relative.sock"]] {
            XCTAssertThrowsError(try DaemonSocket(environment: env)) {
                XCTAssertEqual($0 as? DaemonError, .socketUnconfigured)
                XCTAssertFalse($0.localizedDescription.contains("\n"))
            }
        }
        let socket = try DaemonSocket(environment: ["CASCADE_SOCKET_PATH": "/tmp/exact-widget.sock"])
        XCTAssertEqual(socket.path, "/tmp/exact-widget.sock")
        XCTAssertEqual(socket.connection.endpoint, .unix(path: "/tmp/exact-widget.sock"))
    }
}
