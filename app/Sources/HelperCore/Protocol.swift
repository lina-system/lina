import Foundation

public enum JSONValue: Codable, Equatable, Sendable {
    case null, bool(Bool), number(Double), string(String)
    case array([JSONValue]), object([String: JSONValue])

    public init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() { self = .null }
        else if let x = try? c.decode(Bool.self) { self = .bool(x) }
        else if let x = try? c.decode(Double.self) { self = .number(x) }
        else if let x = try? c.decode(String.self) { self = .string(x) }
        else if let x = try? c.decode([JSONValue].self) { self = .array(x) }
        else { self = .object(try c.decode([String: JSONValue].self)) }
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .null: try c.encodeNil()
        case .bool(let x): try c.encode(x)
        case .number(let x): try c.encode(x)
        case .string(let x): try c.encode(x)
        case .array(let x): try c.encode(x)
        case .object(let x): try c.encode(x)
        }
    }
    public var string: String? { if case .string(let x) = self { return x }; return nil }
    public var object: [String: JSONValue]? { if case .object(let x) = self { return x }; return nil }
}

public enum ErrorCode: String, Codable, Sendable {
    case badRequest = "bad_request", unknownAction = "unknown_action", notAllowed = "not_allowed"
    case permissionMissing = "permission_missing", busy, timeout
    case targetNotFound = "target_not_found", internalError = "internal"
}

public struct HelperError: Error, Codable, Sendable {
    public let code: ErrorCode
    public let detail: String
    public init(_ code: ErrorCode, _ detail: String) { self.code = code; self.detail = detail }
}

public struct Request: Codable, Sendable {
    public let v: Int
    public let id: String
    public let action: String
    public var args: [String: JSONValue]
    public var caller: String?
    public var sshClient: String?
    public var parentPID: Int32?
    enum CodingKeys: String, CodingKey {
        case v, id, action, args, caller
        case sshClient = "ssh_client", parentPID = "parent_pid"
    }
    public init(action: String, args: [String: JSONValue] = [:], id: String = UUID().uuidString,
                caller: String? = nil, sshClient: String? = nil, parentPID: Int32? = nil) {
        self.v = 1; self.id = id; self.action = action; self.args = args
        self.caller = caller; self.sshClient = sshClient; self.parentPID = parentPID
    }
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        v = try c.decode(Int.self, forKey: .v)
        id = try c.decode(String.self, forKey: .id)
        action = try c.decode(String.self, forKey: .action)
        args = c.contains(.args) ? try c.decode([String: JSONValue].self, forKey: .args) : [:]
        caller = try c.decodeIfPresent(String.self, forKey: .caller)
        sshClient = try c.decodeIfPresent(String.self, forKey: .sshClient)
        parentPID = try c.decodeIfPresent(Int32.self, forKey: .parentPID)
    }
    public static func decode(_ data: Data) throws -> Request {
        // JSONDecoder also accepts UTF-16/32; the wire protocol deliberately does not.
        guard String(data: data, encoding: .utf8) != nil, !data.contains(0),
              let request = try? JSONDecoder().decode(Request.self, from: data),
              request.v == 1, !request.id.isEmpty, request.id.utf8.count <= 256,
              !request.action.isEmpty, request.action.utf8.count <= 128,
              (request.caller?.utf8.count ?? 0) <= 256,
              (request.sshClient?.utf8.count ?? 0) <= 256 else {
            throw HelperError(.badRequest, "expected a v1 request object")
        }
        return request
    }
}

public struct Response: Codable, Sendable {
    public let v: Int
    public let id: String
    public let ok: Bool
    public let result: [String: JSONValue]?
    public let error: HelperError?
    public static func success(id: String, result: [String: JSONValue]) -> Self {
        Self(v: 1, id: id, ok: true, result: result, error: nil)
    }
    public static func failure(id: String = "", _ error: HelperError) -> Self {
        Self(v: 1, id: id, ok: false, result: nil, error: error)
    }
    public func line() throws -> Data { try JSONEncoder().encode(self) + Data([10]) }
}

/// The limit includes the terminating newline. Bytes after the first line are ignored:
/// the connection is closed after its single response.
public struct LineFrame {
    public static let limit = 64 * 1024
    private var buffer = Data()
    private let maximumBytes: Int
    public init(maximumBytes: Int = Self.limit) { self.maximumBytes = maximumBytes }
    public mutating func append(_ bytes: Data) throws -> Data? {
        if let end = bytes.firstIndex(of: 10) {
            let prefix = bytes[...end]
            guard buffer.count + prefix.count <= maximumBytes else {
                throw HelperError(.badRequest, "JSON frame exceeds size limit")
            }
            buffer.append(contentsOf: prefix.dropLast())
            return buffer
        }
        guard buffer.count + bytes.count < maximumBytes else {
            throw HelperError(.badRequest, "JSON frame exceeds size limit")
        }
        buffer.append(bytes)
        return nil
    }
}
