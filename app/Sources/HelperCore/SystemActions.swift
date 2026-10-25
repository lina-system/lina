import Foundation
import AppKit
import ApplicationServices
import CoreGraphics

public enum SystemActions {
    public static let version = "0.1.0"
    public static func registry(
        home: String,
        probe: @escaping @Sendable () -> [String: JSONValue] = {
            ["accessibility": .bool(AXIsProcessTrusted()), "screen_recording": .bool(CGPreflightScreenCaptureAccess())]
        },
        openPane: @escaping @MainActor @Sendable (URL) -> Bool = { NSWorkspace.shared.open($0) }
    ) -> [String: Action] {
        let started = ProcessInfo.processInfo.systemUptime
        let alert = ScreenAlertState(path: home + "/screen-recording-first-grant.json")
        return [
            "status": Action { context in
                guard context.args.isEmpty else { throw HelperError(.badRequest, "status takes no arguments") }
                let grants = probe()
                return ["version": .string(version), "pid": .number(Double(ProcessInfo.processInfo.processIdentifier)),
                        "uptime": .number(ProcessInfo.processInfo.systemUptime - started), "grants": .object(grants),
                        "screen_recording": .object(["next_alert": alert.nextAlert(granted: grants["screen_recording"] == .bool(true))])]
            },
            "permissions": Action { context in
                guard Set(context.args.keys).isSubset(of: ["open"]) else {
                    throw HelperError(.badRequest, "unsupported permissions argument")
                }
                if let pane = context.args["open"] {
                    let target: String
                    switch pane.string {
                    case "accessibility": target = "Privacy_Accessibility"
                    case "screen_recording": target = "Privacy_ScreenCapture"
                    default: throw HelperError(.badRequest, "open must be accessibility or screen_recording")
                    }
                    let done = DispatchSemaphore(value: 0)
                    let opened = Locked(false)
                    DispatchQueue.main.async {
                        defer { done.signal() }
                        guard (try? context.checkDeadline()) != nil else { return }
                        let url = URL(string: "x-apple.systempreferences:com.apple.preference.security?" + target)!
                        opened.withLock { $0 = openPane(url) }
                    }
                    let remaining = max(0, context.deadline - ProcessInfo.processInfo.systemUptime)
                    guard done.wait(timeout: .now() + remaining) == .success else {
                        throw HelperError(.timeout, "opening Settings timed out")
                    }
                    guard opened.withLock({ $0 }) else { throw HelperError(.internalError, "could not open System Settings") }
                }
                return probe()
            },
        ]
    }
}

/// This is an estimate from the first observed grant, not a read of TCC or replayd.
private final class ScreenAlertState: Sendable {
    private let path: String
    private let first: Locked<Date?>
    init(path: String) {
        self.path = path
        let data = try? Data(contentsOf: URL(fileURLWithPath: path))
        first = Locked(data.flatMap { try? JSONDecoder().decode(Date.self, from: $0) })
    }
    func nextAlert(granted: Bool) -> JSONValue {
        guard granted else { return .null }
        return first.withLock { date in
            if date == nil {
                let now = Date()
                // Do not report an estimate that cannot survive the next restart.
                guard let data = try? JSONEncoder().encode(now),
                      (try? data.write(to: URL(fileURLWithPath: path), options: .atomic)) != nil else { return .null }
                date = now
            }
            return .string(ISO8601DateFormatter().string(from: date!.addingTimeInterval(30 * 86400)))
        }
    }
}
