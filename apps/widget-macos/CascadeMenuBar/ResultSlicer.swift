import Foundation

// Purpose: retain the exact top-level result bytes, including whitespace inside it.
// Inputs: valid RPC JSON. Outputs: raw member slice. Constraints: never re-encode.
enum ResultSlicer {
    static func result(in data: Data) throws -> Data {
        guard let object = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              object["error"] == nil, object["result"] is [String: Any] else { throw DaemonError.invalidResponse }
        let bytes = Array(data)
        var offset = 0
        skipWhitespace(bytes, &offset)
        guard offset < bytes.count, bytes[offset] == 123 else { throw DaemonError.invalidResponse }
        offset += 1
        while offset < bytes.count {
            skipWhitespace(bytes, &offset)
            if bytes[offset] == 125 { break }
            let keyStart = offset
            try skipString(bytes, &offset)
            let key = try JSONDecoder().decode(String.self, from: Data(bytes[keyStart..<offset]))
            skipWhitespace(bytes, &offset)
            guard offset < bytes.count, bytes[offset] == 58 else { throw DaemonError.invalidResponse }
            offset += 1
            skipWhitespace(bytes, &offset)
            let start = offset
            try skipValue(bytes, &offset)
            if key == "result" { return Data(bytes[start..<offset]) }
            skipWhitespace(bytes, &offset)
            if offset < bytes.count, bytes[offset] == 44 { offset += 1 }
        }
        throw DaemonError.invalidResponse
    }

    private static func skipWhitespace(_ bytes: [UInt8], _ offset: inout Int) {
        while offset < bytes.count && [9, 10, 13, 32].contains(bytes[offset]) { offset += 1 }
    }

    private static func skipString(_ bytes: [UInt8], _ offset: inout Int) throws {
        guard offset < bytes.count, bytes[offset] == 34 else { throw DaemonError.invalidResponse }
        offset += 1
        while offset < bytes.count {
            let value = bytes[offset]
            offset += 1
            if value == 92 { offset += 1 }
            else if value == 34 { return }
        }
        throw DaemonError.invalidResponse
    }

    private static func skipValue(_ bytes: [UInt8], _ offset: inout Int) throws {
        var depth = 0
        while offset < bytes.count {
            let value = bytes[offset]
            if value == 34 { try skipString(bytes, &offset); continue }
            if value == 123 || value == 91 { depth += 1 }
            if value == 125 || value == 93 {
                if depth == 0 { return }
                depth -= 1
                if depth == 0 { offset += 1; return }
            }
            if depth == 0 && (value == 44 || [9, 10, 13, 32].contains(value)) { return }
            offset += 1
        }
    }
}
