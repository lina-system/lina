import Foundation
import Darwin
import ApplicationServices
import CoreGraphics
import OSLog

struct CuaGrants: Equatable, Sendable {
    var accessibility: Bool
    var screenRecording: Bool
}

struct CuaDependencies: Sendable {
    var policy: @Sendable () -> Policy
    var grants: @Sendable () -> CuaGrants
    var now: @Sendable () -> TimeInterval
    var sleep: @Sendable (TimeInterval) -> Void
    var spawn: @Sendable (CuaLaunch) throws -> pid_t
    var verify: @Sendable (pid_t) throws -> Void
    var signal: @Sendable (pid_t, Int32) -> Void
    var reap: @Sendable (pid_t, Bool) -> Bool
    var socketReady: @Sendable (String) -> Bool
    var query: @Sendable (CuaLaunch) throws -> String
    var audit: @Sendable (String) -> Void
}

/// A single serial owner of the daemon PID. No asynchronous callback can start a
/// replacement before the previous child is reaped. Readiness is polled, so policy
/// changes and shutdown can interrupt startup without waiting for its 10 s deadline.
public final class CuaDriverSupervisor: @unchecked Sendable {
    private let queue = DispatchQueue(label: "app.lina-os.helper.cua")
    private let deps: CuaDependencies
    private let home: String
    private let executable: String
    private let gate: CuaGate?
    private var timer: DispatchSourceTimer?
    private var policy = Policy.restricted
    private var grants: CuaGrants?
    private var lastCheck: TimeInterval = -.infinity
    private var pid: pid_t?
    private var started: TimeInterval = 0
    private var ready = false
    private var blocked = false
    private var closed = false
    private var ownsFiles = false
    private var nextStart: TimeInterval = 0
    private var backoff: TimeInterval = 1
    private var launches = 0
    private var lastError: String?
    private var permissions: JSONValue = .null
    private var socket: String { home + "/run/cua-daemon.sock" }
    private var gateSocket: String { home + "/run/cua.sock" }
    private var policyFile: String { home + "/run/cua-policy.yaml" }

    public convenience init(home: String, policyPath: String, audit: AuditLog, development: Bool = false) {
        let executable = Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/cua-driver").path
        self.init(home: home, executable: executable, dependencies: CuaDependencies(
            policy: { Policy.load(path: policyPath) },
            grants: { CuaGrants(accessibility: AXIsProcessTrusted(), screenRecording: CGPreflightScreenCaptureAccess()) },
            now: { ProcessInfo.processInfo.systemUptime }, sleep: { Thread.sleep(forTimeInterval: $0) },
            spawn: { try CuaProcess.spawn($0) }, verify: CuaProcess.verify,
            signal: { _ = kill($0, $1) }, reap: { CuaProcess.reap($0, wait: $1) },
            socketReady: CuaProcess.socketReady, query: CuaProcess.query,
            audit: { event in
                do { try audit.recordCua(event) }
                catch { Logger(subsystem: "app.lina-os.helper", category: "cua").error("cua audit write failed") }
            }), gate: CuaGate(home: home, audit: audit, development: development))
    }

    init(home: String, executable: String, dependencies: CuaDependencies, gate: CuaGate? = nil) {
        self.home = home; self.executable = executable; deps = dependencies; self.gate = gate
    }

    public func startMonitoring() {
        queue.sync {
            guard timer == nil, !closed else { return }
            // Monitoring begins only after HelperServer has acquired helper.lock.
            if !ownsFiles {
                ownsFiles = true
                cleanup()
                do { try CuaGate.removeStaleSocket(gateSocket) }
                catch { deps.audit("cannot remove stale cua gate socket") }
            }
            refresh(); tick()
            let source = DispatchSource.makeTimerSource(queue: queue)
            source.schedule(deadline: .now() + .milliseconds(100), repeating: .milliseconds(100))
            source.setEventHandler { [weak self] in self?.pollLocked() }
            timer = source; source.resume()
        }
    }

    /// Loads once on this queue and returns the very policy used for the request.
    /// Re-reading here avoids applying an old request's snapshot after a timer update.
    @discardableResult public func checkPolicy() -> Policy {
        queue.sync { if !closed { refresh(); tick() }; return policy }
    }

    func poll() { queue.sync { pollLocked() } }
    private func pollLocked() {
        guard !closed else { return }
        if deps.now() - lastCheck >= 5 { refresh() }
        tick()
    }

    private func refresh() {
        let current = deps.policy()
        let currentGrants = deps.grants()
        lastCheck = deps.now()
        if current.profile != policy.profile {
            stopChild()
            blocked = false; backoff = 1; nextStart = 0; lastError = nil
            policy = current
        } else { policy = current }
        if let grants, grants != currentGrants, pid != nil {
            deps.audit("grants changed; restarting cua-driver")
            stopChild(); nextStart = 0; backoff = 1
        }
        grants = currentGrants
    }

    private func tick() {
        guard !closed, policy.runsCuaDriver, !blocked else { return }
        if let pid, deps.reap(pid, false) {
            self.pid = nil; ready = false; permissions = .null
            cleanup()
            if deps.now() - started >= 300 { backoff = 1 }
            failure("cua-driver exited unexpectedly")
        }
        if pid == nil, deps.now() >= nextStart { spawn() }
        guard let pid, !ready else { return }
        if deps.now() - started >= 10 {
            stopChild(); failure("cua socket readiness timed out")
        } else if deps.socketReady(socket) {
            unlink(policyFile)
            do {
                let output = try deps.query(launch(["status", "--socket", socket]))
                guard CuaPolicy.statusDigest(output) == CuaPolicy.digest(CuaPolicy.yaml(profile: "operate")) else {
                    reject("cua policy hash mismatch"); return
                }
                // Best-effort, read-only probe. Preserve source.attribution from the daemon.
                do {
                    let text = try deps.query(launch(["call", "--socket", socket, "check_permissions", "{}"]))
                    permissions = (try? JSONDecoder().decode(JSONValue.self, from: Data(text.utf8))) ?? .null
                } catch let error as CuaSignatureError { throw error }
                catch { permissions = .null }
                if deps.reap(pid, false) {
                    self.pid = nil; cleanup(); failure("cua-driver exited during readiness"); return
                }
                try gate?.start()
                ready = true; lastError = nil
                deps.audit("cua-driver ready pid=\(pid)")
            } catch let error as CuaSignatureError {
                reject(error.detail)
            } catch {
                stopChild(); failure((error as? HelperError)?.detail ?? "cua status failed")
            }
        }
    }

    private func spawn() {
        do {
            try PrivateFiles.directory(home + "/run")
            ownsFiles = true
            cleanup()
            try CuaPolicy.write(CuaPolicy.yaml(profile: "operate"), path: policyFile)
            let child = try deps.spawn(launch(["serve", "--embedded", "--socket", socket, "--no-permissions-gate",
                "--permission-mode", "standard", "--host-bundle-id", "app.lina-os.helper"], policy: true))
            pid = child; launches += 1; started = deps.now()
            do { try deps.verify(child) }
            catch {
                reject((error as? CuaSignatureError)?.detail ?? (error as? HelperError)?.detail ?? "cua signature check failed")
                return
            }
            deps.signal(child, SIGCONT)
            deps.audit("cua-driver resumed pid=\(child)")
        } catch {
            cleanup(); failure((error as? HelperError)?.detail ?? "cua spawn failed")
        }
    }

    private func launch(_ arguments: [String], policy: Bool = false) -> CuaLaunch {
        let inherited = ProcessInfo.processInfo.environment
        var environment = ["HOME": FileManager.default.homeDirectoryForCurrentUser.path, "USER": NSUserName(),
            "PATH": "/usr/bin:/bin", "TMPDIR": inherited["TMPDIR"] ?? NSTemporaryDirectory(),
            "CUA_DRIVER_EMBEDDED": "1", "CUA_DRIVER_HOST_BUNDLE_ID": "app.lina-os.helper", "CUA_DRIVER_RS_TELEMETRY_ENABLED": "0"]
        if policy { environment["CUA_DRIVER_POLICY_FILE"] = policyFile }
        else {
            // v0.38.0 otherwise self-execs finite commands even with telemetry disabled.
            // Skip that unchecked second exec for our verified, short-lived CLI queries.
            environment["CUA_DRIVER_CLI_TELEMETRY_CHILD"] = "1"
        }
        return CuaLaunch(executable: executable, arguments: arguments, environment: environment)
    }

    private func failure(_ message: String) {
        lastError = message; nextStart = deps.now() + backoff
        deps.audit("\(message); retry in \(backoff)s")
        backoff = min(60, backoff * 2)
    }

    private func reject(_ message: String) {
        gate?.stop()
        if let pid { deps.signal(pid, SIGKILL); _ = deps.reap(pid, true) }
        pid = nil; ready = false; permissions = .null; blocked = true; lastError = message
        cleanup(); deps.audit(message + "; blocked until profile change or cua.restart")
    }

    private func stopChild() {
        gate?.stop()
        if let child = pid {
            // Reap before signalling so an already-observed exit cannot target a reused PID.
            if !deps.reap(child, false) {
                deps.signal(child, SIGTERM)
                let deadline = deps.now() + 3
                var exited = deps.reap(child, false)
                while !exited && deps.now() < deadline {
                    deps.sleep(0.01); exited = deps.reap(child, false)
                }
                if !exited { deps.signal(child, SIGKILL); _ = deps.reap(child, true) }
            }
            deps.audit("cua-driver stopped pid=\(child)")
        }
        pid = nil; ready = false; permissions = .null; cleanup()
    }

    private func cleanup() {
        gate?.stop()
        guard ownsFiles else { return }
        unlink(policyFile); unlink(socket)
    }

    public func restart() throws {
        try queue.sync {
            guard !closed else { throw HelperError(.notAllowed, "helper is shutting down") }
            refresh()
            guard policy.runsCuaDriver else { throw HelperError(.notAllowed, "cua-driver requires operate") }
            stopChild(); blocked = false; backoff = 1; nextStart = 0
            tick()
        }
    }

    public func status() -> [String: JSONValue] {
        queue.sync {
            ["running": .bool(ready), "pid": pid.map { .number(Double($0)) } ?? .null,
             "version": .string(CuaDriverPin.version), "socket": .string(gateSocket),
             "gate_socket": .string(gateSocket), "daemon_socket": .string(socket),
             "last_error": lastError.map(JSONValue.string) ?? .null, "restarts": .number(Double(max(0, launches - 1))),
             "check_permissions": permissions,
             "message": .string(policy.runsCuaDriver ? (ready ? "running" : "not running") : "not running under \(policy.profile)")]
        }
    }

    public func shutdown() {
        queue.sync { closed = true; timer?.cancel(); timer = nil; stopChild(); ownsFiles = false }
    }
    deinit { timer?.cancel(); stopChild() }
}
