import Foundation

/// Shared validation only; each action owns its argument schema.
enum ActionArguments {
    static func keys(_ context: ActionContext, _ allowed: Set<String>) throws {
        guard Set(context.args.keys).isSubset(of: allowed) else { throw HelperError(.badRequest, "unknown argument") }
    }
    static func string(_ value: JSONValue?, name: String) throws -> String {
        guard let text = value?.string, !text.isEmpty else { throw HelperError(.badRequest, "\(name) must be a nonempty string") }
        return text
    }
    static func number(_ value: JSONValue?, name: String) throws -> Double {
        guard case .number(let number) = value, number.isFinite else { throw HelperError(.badRequest, "\(name) must be a finite number") }
        return number
    }
    static func integer(_ value: JSONValue?, name: String, range: ClosedRange<Int>) throws -> Int {
        let number = try number(value, name: name)
        guard let integer = Int(exactly: number), range.contains(integer) else {
            throw HelperError(.badRequest, "\(name) outside allowed range")
        }
        return integer
    }
    static func bundleID(_ value: JSONValue?) throws -> String {
        let text = try string(value, name: "bundle_id")
        let parts = text.split(separator: ".", omittingEmptySubsequences: false)
        guard text.utf8.count <= 255, parts.allSatisfy({ !$0.isEmpty }),
              text.utf8.allSatisfy({ (65...90).contains($0) || (97...122).contains($0) || (48...57).contains($0) || [45, 46, 95].contains($0) }) else {
            throw HelperError(.badRequest, "invalid bundle ID")
        }
        return text
    }
    static func app(_ value: JSONValue?) throws -> String {
        if value == .string("frontmost") { return "frontmost" }
        return try bundleID(value)
    }
    static func grant(_ name: String, probe: @Sendable () -> [String: JSONValue]) throws {
        guard probe()[name] == .bool(true) else { throw HelperError(.permissionMissing, name) }
    }
}

/// Bridge framework callbacks to the dispatcher's bounded synchronous workers.
/// Callbacks only publish values; actions check the deadline before side effects.
final class ActionCompletion<Value: Sendable>: Sendable {
    private let value = Locked<Result<Value, HelperError>?>(nil)
    private let done = DispatchSemaphore(value: 0)
    func finish(_ result: Result<Value, HelperError>) {
        value.withLock { $0 = result }
        done.signal()
    }
    func wait(_ context: ActionContext) throws -> Value {
        // The dispatcher returns timeout to the client. Keep this worker's slot
        // until the framework callback finishes, preventing abandoned captures
        // or launches from accumulating after repeated short policy timeouts.
        done.wait()
        try context.checkDeadline()
        return try value.withLock { try $0!.get() }
    }
}
