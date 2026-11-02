import Foundation

public struct ActionContext: Sendable {
    public let args: [String: JSONValue]
    public let limits: [String: JSONValue]
    public let deadline: TimeInterval
    public func checkDeadline() throws {
        if ProcessInfo.processInfo.systemUptime >= deadline { throw HelperError(.timeout, "action deadline exceeded") }
    }
}

public struct Action: Sendable {
    public let timeout: TimeInterval
    public let run: @Sendable (ActionContext) throws -> [String: JSONValue]
    public init(timeout: TimeInterval = 10, run: @escaping @Sendable (ActionContext) throws -> [String: JSONValue]) {
        self.timeout = timeout; self.run = run
    }
}

/// Action files register handlers here; they receive the trusted policy's limits.
/// Timed-out work retains its slot until it exits, so deadlines cannot create
/// unlimited abandoned workers. Handlers must check deadlines before side effects.
public final class ActionDispatcher: Sendable {
    private let actions: [String: Action]
    private let slots: DispatchSemaphore
    public init(actions: [String: Action], capacity: Int = 4) {
        self.actions = actions; slots = DispatchSemaphore(value: capacity)
    }
    public func dispatch(_ request: Request, policy: Policy) -> Response {
        guard let action = actions[request.action] else {
            return .failure(id: request.id, HelperError(.unknownAction, "unknown action"))
        }
        guard policy.allows(request.action) else {
            return .failure(id: request.id, HelperError(.notAllowed, "action disabled by policy"))
        }
        let limits = policy.limits(for: request.action)
        var timeout = action.timeout
        if let value = limits["timeout_s"] {
            guard case .number(let seconds) = value, seconds.isFinite, seconds > 0 else {
                return .failure(id: request.id, HelperError(.badRequest, "invalid policy timeout_s"))
            }
            // Policy can tighten the action's built-in upper bound, never widen it.
            timeout = min(timeout, seconds)
        }
        guard slots.wait(timeout: .now()) == .success else {
            return .failure(id: request.id, HelperError(.busy, "action capacity reached"))
        }
        let completion = DispatchSemaphore(value: 0)
        let result = Locked<Response?>(nil)
        let context = ActionContext(args: request.args, limits: limits,
                                    deadline: ProcessInfo.processInfo.systemUptime + timeout)
        DispatchQueue.global().async { [slots] in
            defer { slots.signal(); completion.signal() }
            let response: Response
            do {
                try context.checkDeadline()
                var value = try action.run(context)
                if request.action == "status" { value["policy"] = .object(policy.metadata) }
                try context.checkDeadline()
                response = .success(id: request.id, result: value)
            } catch let error as HelperError { response = .failure(id: request.id, error) }
            catch { response = .failure(id: request.id, HelperError(.internalError, "action failed")) }
            result.withLock { $0 = response }
        }
        guard completion.wait(timeout: .now() + timeout) == .success else {
            return .failure(id: request.id, HelperError(.timeout, "action deadline exceeded"))
        }
        return result.withLock { $0! }
    }
}

final class Locked<Value>: @unchecked Sendable {
    private let lock = NSLock()
    private var value: Value
    init(_ value: Value) { self.value = value }
    func withLock<T>(_ body: (inout Value) throws -> T) rethrows -> T {
        lock.lock(); defer { lock.unlock() }
        return try body(&value)
    }
}
