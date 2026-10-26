import Foundation

/// One in-memory lease shared by helper actions and the gate. Monotonic time
/// avoids wall-clock adjustments changing the duration of an acquired lease.
final class CuaInputLease: Sendable {
    struct Snapshot {
        let holder: String
        let seconds: Int
        var json: [String: JSONValue] {
            ["holder": .string(holder), "expires_in_s": .number(Double(seconds))]
        }
        var detail: String { "\(holder) is using this Mac (\(seconds)s left)" }
    }
    private struct Lease {
        let holder: String
        let expires: TimeInterval
    }
    private let state = Locked<Lease?>(nil)
    private let now: @Sendable () -> TimeInterval

    init(now: @escaping @Sendable () -> TimeInterval = { ProcessInfo.processInfo.systemUptime }) {
        self.now = now
    }

    private static func validate(_ holder: String) throws {
        guard (1...64).contains(holder.utf8.count), holder.utf8.allSatisfy({
            (65...90).contains($0) || (97...122).contains($0) || (48...57).contains($0) ||
                [95, 46, 58, 64, 45].contains($0)
        }) else { throw HelperError(.badRequest, "holder must be 1..64 characters from [A-Za-z0-9_.:@-]") }
    }

    private func active(_ lease: inout Lease?, at time: TimeInterval) -> Snapshot? {
        guard let value = lease else { return nil }
        guard value.expires > time else { lease = nil; return nil }
        return Snapshot(holder: value.holder, seconds: Int(ceil(value.expires - time)))
    }

    func grant(holder: String, ttl: TimeInterval = 300) throws -> [String: JSONValue] {
        try Self.validate(holder)
        guard ttl.isFinite, ttl > 0, ttl <= 300 else {
            throw HelperError(.badRequest, "ttl_s must be greater than 0 and at most 300")
        }
        return try state.withLock { lease in
            let time = now()
            if let current = active(&lease, at: time), current.holder != holder {
                throw HelperError(.busy, current.detail)
            }
            lease = Lease(holder: holder, expires: time + ttl)
            return Snapshot(holder: holder, seconds: Int(ceil(ttl))).json
        }
    }

    func release(holder: String) throws {
        try Self.validate(holder)
        try state.withLock { lease in
            if let current = active(&lease, at: now()), current.holder != holder {
                throw HelperError(.busy, current.detail)
            }
            lease = nil
        }
    }

    func status() -> JSONValue {
        state.withLock { active(&$0, at: now()).map { .object($0.json) } ?? .null }
    }

    /// Checking input never extends a lease. Reads do not call this method.
    func refusal(identity: String?) -> Snapshot? {
        state.withLock { lease in
            guard let current = active(&lease, at: now()), current.holder != identity else { return nil }
            return current
        }
    }
}
