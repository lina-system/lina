import Foundation
import CryptoKit
import OSLog
import Darwin

public final class AuditLog: @unchecked Sendable {
    private let directory: String
    private let lock = NSLock()
    private var lastCleanup: Date
    private let logger = Logger(subsystem: "app.lina-os.helper", category: "audit")

    public init(directory: String, now: Date = Date()) throws {
        self.directory = directory; self.lastCleanup = now
        try PrivateFiles.directory(directory)
        try cleanup(now: now)
    }

    /// Redact every string argument, plus binary/content arrays and objects, including
    /// nested values. An unknown future argument must not accidentally leak content.
    public static func redact(_ args: [String: JSONValue]) -> [String: JSONValue] {
        var result: [String: JSONValue] = [:]
        var fingerprints: [String: JSONValue] = [:]
        let contentKeys: Set<String> = ["text", "content", "contents", "file_contents", "data", "pixels", "bytes"]
        for (key, value) in args {
            if let string = value.string {
                digest(Data(string.utf8), key: key, into: &fingerprints)
            } else if contentKeys.contains(key.lowercased()) {
                let encoder = JSONEncoder(); encoder.outputFormatting = [.sortedKeys]
                digest((try? encoder.encode(value)) ?? Data(), key: key, into: &fingerprints)
            } else if let object = value.object {
                result[key] = .object(redact(object))
            } else if case .array(let items) = value {
                result[key] = .array(items.map { .object(redact(["value": $0])) })
            } else { result[key] = value }
        }
        return result.merging(fingerprints) { _, fingerprint in fingerprint }
    }

    private static func digest(_ data: Data, key: String, into result: inout [String: JSONValue]) {
        result[key + "_len"] = .number(Double(data.count))
        result[key + "_sha256"] = .string(SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined())
    }

    public func cleanup(now: Date = Date()) throws {
        lock.lock(); defer { lock.unlock() }
        try cleanupLocked(now: now)
    }
    private func cleanupLocked(now: Date) throws {
        let urls = try FileManager.default.contentsOfDirectory(at: URL(fileURLWithPath: directory), includingPropertiesForKeys: nil)
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX"); formatter.timeZone = .current
        formatter.dateFormat = "yyyyMMdd"; formatter.isLenient = false
        for url in urls {
            let name = url.lastPathComponent
            guard name.range(of: #"^audit-[0-9]{8}\.jsonl$"#, options: .regularExpression) != nil else { continue }
            let day = String(name.dropFirst(6).prefix(8))
            guard let date = formatter.date(from: day),
                  date < Calendar.current.startOfDay(for: now).addingTimeInterval(-30 * 86400) else { continue }
            var info = stat()
            if lstat(url.path, &info) == 0, info.st_mode & S_IFMT == S_IFREG, info.st_uid == getuid() {
                guard unlink(url.path) == 0 else { throw HelperError(.internalError, "audit cleanup failed") }
            }
        }
        lastCleanup = now
    }

    public func record(request: Request?, peer: Peer, response: Response, elapsed: Double, now: Date = Date()) throws {
        lock.lock(); defer { lock.unlock() }
        if !Calendar.current.isDate(now, inSameDayAs: lastCleanup) { try cleanupLocked(now: now) }
        let iso = ISO8601DateFormatter(); iso.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        var peerJSON: [String: JSONValue] = ["uid": .number(Double(peer.uid))]
        if let pid = peer.pid { peerJSON["pid"] = .number(Double(pid)) }
        if let exe = peer.executable { peerJSON["exe"] = .string(exe) }
        if let team = peer.identity.team { peerJSON["team"] = .string(team) }
        if let ssh = request?.sshClient { peerJSON["ssh"] = .string(ssh) }
        if let parent = request?.parentPID { peerJSON["parent_pid"] = .number(Double(parent)) }
        var entry: [String: JSONValue] = ["t": .string(iso.string(from: now)), "id": .string(request?.id ?? ""),
            "action": .string(request?.action ?? ""), "caller": request?.caller.map(JSONValue.string) ?? .null,
            "peer": .object(peerJSON), "args": .object(Self.redact(request?.args ?? [:])),
            "ok": .bool(response.ok), "ms": .number(elapsed * 1000)]
        if let error = response.error { entry["error"] = .string(error.code.rawValue) }
        try appendLocked(entry, now: now)
        logger.info("request action=\(request?.action ?? "invalid", privacy: .private) ok=\(response.ok) code=\(response.error?.code.rawValue ?? "ok", privacy: .public)")
    }

    /// One entry before each gate request is forwarded, or for a refusal. No reply
    /// content is recorded. A write error must prevent forwarding at the caller.
    public func recordCuaRequest(_ request: [String: JSONValue]?, peer: Peer,
                                 error: String? = nil, now: Date = Date()) throws {
        lock.lock(); defer { lock.unlock() }
        if !Calendar.current.isDate(now, inSameDayAs: lastCleanup) { try cleanupLocked(now: now) }
        let iso = ISO8601DateFormatter(); iso.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let method = request?["method"]?.string ?? "unknown"
        let action = method == "call" ? "cua.call:" + (request?["name"]?.string ?? "unknown") : "cua." + method
        var peerJSON: [String: JSONValue] = ["uid": .number(Double(peer.uid))]
        if let pid = peer.pid { peerJSON["pid"] = .number(Double(pid)) }
        if let exe = peer.executable { peerJSON["exe"] = .string(exe) }
        if let team = peer.identity.team { peerJSON["team"] = .string(team) }
        if let parent = peer.parentPID { peerJSON["parent_pid"] = .number(Double(parent)) }
        if let exe = peer.parentExecutable { peerJSON["parent_exe"] = .string(exe) }
        // Opaque methods may use non-object args; redact those too.
        let args = request?["args"].map { $0.object ?? ["value": $0] } ?? [:]
        var entry: [String: JSONValue] = ["t": .string(iso.string(from: now)), "id": .string(UUID().uuidString),
            "action": .string(action), "peer": .object(peerJSON), "args": .object(Self.redact(args)),
            "session_id": request?["session_id"]?.string.map(JSONValue.string) ?? .null, "ok": .bool(error == nil)]
        if let error { entry["error"] = .string(error) }
        try appendLocked(entry, now: now)
        logger.info("cua request action=\(action, privacy: .private) ok=\(error == nil)")
    }

    /// Supervisor messages contain only internal lifecycle reasons and PIDs, no tool arguments.
    public func recordCua(_ event: String, now: Date = Date()) throws {
        lock.lock(); defer { lock.unlock() }
        if !Calendar.current.isDate(now, inSameDayAs: lastCleanup) { try cleanupLocked(now: now) }
        let iso = ISO8601DateFormatter(); iso.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        try appendLocked(["t": .string(iso.string(from: now)), "action": .string("cua.lifecycle"),
                          "event": .string(event)], now: now)
        logger.info("cua lifecycle: \(event, privacy: .public)")
    }

    private func appendLocked(_ entry: [String: JSONValue], now: Date) throws {
        let formatter = DateFormatter(); formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.dateFormat = "yyyyMMdd"
        let path = directory + "/audit-" + formatter.string(from: now) + ".jsonl"
        let fd = open(path, O_WRONLY | O_CREAT | O_APPEND | O_NOFOLLOW | O_NONBLOCK | O_CLOEXEC, 0o600)
        guard fd >= 0 else { throw HelperError(.internalError, "cannot open audit log") }
        defer { close(fd) }
        var info = stat()
        guard fstat(fd, &info) == 0, info.st_mode & S_IFMT == S_IFREG,
              info.st_uid == getuid(), info.st_nlink == 1, fchmod(fd, 0o600) == 0 else {
            throw HelperError(.internalError, "unsafe audit log")
        }
        let data = try JSONEncoder().encode(entry) + Data([10])
        try data.withUnsafeBytes { bytes in
            var offset = 0
            while offset < bytes.count {
                let n = write(fd, bytes.baseAddress!.advanced(by: offset), bytes.count - offset)
                if n < 0 && errno == EINTR { continue }
                guard n > 0 else { throw HelperError(.internalError, "cannot write audit log") }
                offset += n
            }
        }
    }
}
