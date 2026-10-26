import XCTest
import Darwin
@testable import HelperCore

private final class CuaFixture: @unchecked Sendable {
    var policy = Policy.observe
    var grants = CuaGrants(accessibility: false, screenRecording: false)
    var now: TimeInterval = 0
    var events: [String] = []
    var launches: [CuaLaunch] = []
    var dead: Set<pid_t> = []
    var signatureOK = true
    var ready = true
    var permissionsSignatureOK = true
    var ignoresTerm = false
    var nextPID: pid_t = 100
    let home: String
    var supervisor: CuaDriverSupervisor!
    init(home: String = "/tmp/cs-" + UUID().uuidString, gate: CuaGate? = nil) {
        self.home = home
        supervisor = CuaDriverSupervisor(home: home, executable: "/test/cua-driver", dependencies: CuaDependencies(
            policy: { [self] in policy }, grants: { [self] in grants }, now: { [self] in now },
            sleep: { [self] in now += $0 },
            spawn: { [self] launch in
                launches.append(launch); nextPID += 1; events.append("spawn \(nextPID)"); return nextPID
            },
            verify: { [self] pid in
                events.append("verify \(pid)")
                if !signatureOK { throw HelperError(.internalError, "signature refused") }
            },
            signal: { [self] pid, signal in
                events.append("signal \(pid) \(signal)")
                if signal == SIGKILL || (signal == SIGTERM && !ignoresTerm) { dead.insert(pid) }
            },
            reap: { [self] pid, wait in
                if dead.contains(pid) { events.append("reap \(pid)"); return true }
                XCTAssertFalse(wait); return false
            },
            socketReady: { [self] path in
                XCTAssertEqual(path, home + "/run/cua-daemon.sock"); return ready
            },
            query: { [self] launch in
                XCTAssertEqual(launch.arguments[2], home + "/run/cua-daemon.sock")
                XCTAssertNil(launch.environment["CUA_DRIVER_POLICY_FILE"])
                XCTAssertEqual(launch.environment["CUA_DRIVER_CLI_TELEMETRY_CHILD"], "1")
                XCTAssertEqual(launch.arguments, ["call", "--socket", home + "/run/cua-daemon.sock", "check_permissions", "{}"])
                if !permissionsSignatureOK { throw CuaSignatureError(detail: "permission query signature refused") }
                return #"{"source":{"attribution":"host"}}"#
            },
            audit: { [self] event in events.append("audit " + event) }
        ), gate: gate, lease: CuaInputLease(now: { [self] in now }))
    }
    func operate() { policy = Policy.parse(Data(#"{"profile":"operate"}"#.utf8), owner: 0, mode: 0o644) }
    func close() { supervisor.shutdown(); supervisor = nil; try? FileManager.default.removeItem(atPath: home) }
}

final class CuaDriverTests: XCTestCase {
    private func fixture() -> CuaFixture {
        let f = CuaFixture()
        addTeardownBlock { f.close() }
        return f
    }

    func testOnlyOperateStartsAndObserveStops() throws {
        let f = fixture()
        for policy in [Policy.observe, .restricted, Policy.parse(Data(#"{"actions":{"cua.lease":{}}}"#.utf8), owner: 0, mode: 0o644)] {
            f.policy = policy; _ = f.supervisor.checkPolicy(); XCTAssertTrue(f.launches.isEmpty)
        }
        f.operate(); _ = f.supervisor.checkPolicy()
        XCTAssertEqual(f.events.prefix(3), ["spawn 101", "verify 101", "signal 101 \(SIGCONT)"])
        XCTAssertEqual(f.supervisor.status()["running"], .bool(true))
        f.policy = .observe; _ = f.supervisor.checkPolicy()
        XCTAssertTrue(f.events.contains("signal 101 \(SIGTERM)"))
        XCTAssertTrue(f.events.contains("reap 101"))
        XCTAssertEqual(f.supervisor.status()["message"], .string("not running under observe"))
    }

    func testGateAbsentDuringObserveAndReadinessWait() throws {
        let home = "/tmp/cs-" + UUID().uuidString
        let gate = CuaGate(home: home, audit: try AuditLog(directory: home + "/logs"))
        let f = CuaFixture(home: home, gate: gate)
        defer { f.close() }
        _ = f.supervisor.checkPolicy()
        XCTAssertFalse(FileManager.default.fileExists(atPath: home + "/run/cua.sock"))
        XCTAssertFalse(FileManager.default.fileExists(atPath: home + "/run/cua-daemon.sock"))
        f.ready = false; f.operate(); _ = f.supervisor.checkPolicy()
        XCTAssertFalse(FileManager.default.fileExists(atPath: home + "/run/cua.sock"))
        f.now = 10; f.supervisor.poll()
        XCTAssertFalse(FileManager.default.fileExists(atPath: home + "/run/cua.sock"))
        f.policy = .observe; _ = f.supervisor.checkPolicy()
        XCTAssertFalse(FileManager.default.fileExists(atPath: home + "/run/cua-daemon.sock"))
    }

    func testGateReadyExitRestartAndObserveLifecycle() throws {
        let home = "/tmp/cs-" + UUID().uuidString
        let gate = CuaGate(home: home, audit: try AuditLog(directory: home + "/logs"))
        let f = CuaFixture(home: home, gate: gate)
        defer { f.close() }
        // Probe only the environment restriction; no assertions are relaxed.
        do { try gate.start(); gate.stop() }
        catch let error as HelperError where error.detail == "cannot bind cua gate (errno 1)" {
            throw XCTSkip("environment prohibits Unix listener bind (EPERM)")
        }
        f.ready = false; f.operate(); _ = f.supervisor.checkPolicy()
        XCTAssertFalse(FileManager.default.fileExists(atPath: home + "/run/cua.sock"))
        // Represent the fake process's internal endpoint with a real Unix socket.
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        defer { close(fd) }
        XCTAssertEqual(try Transport.withAddress(home + "/run/cua-daemon.sock") { Darwin.bind(fd, $0, $1) }, 0)
        f.ready = true; f.supervisor.poll()
        XCTAssertTrue(FileManager.default.fileExists(atPath: home + "/run/cua.sock"))
        f.dead.insert(f.nextPID); f.supervisor.poll()
        XCTAssertFalse(FileManager.default.fileExists(atPath: home + "/run/cua.sock"))
        XCTAssertFalse(FileManager.default.fileExists(atPath: home + "/run/cua-daemon.sock"))
        f.now += 1; f.supervisor.poll()
        XCTAssertTrue(FileManager.default.fileExists(atPath: home + "/run/cua.sock"))
        try f.supervisor.restart()
        XCTAssertTrue(FileManager.default.fileExists(atPath: home + "/run/cua.sock"))
        f.policy = .observe; _ = f.supervisor.checkPolicy()
        XCTAssertFalse(FileManager.default.fileExists(atPath: home + "/run/cua.sock"))
        XCTAssertFalse(FileManager.default.fileExists(atPath: home + "/run/cua-daemon.sock"))
    }

    func testLaunchContractAndNoInheritedConfiguration() {
        let f = fixture(); f.operate(); _ = f.supervisor.checkPolicy()
        let launch = f.launches[0]
        XCTAssertEqual(launch.arguments, ["serve", "--embedded", "--socket", f.home + "/run/cua-daemon.sock",
            "--no-permissions-gate", "--permission-mode", "standard", "--host-bundle-id", "app.lina-os.helper"])
        XCTAssertEqual(Set(launch.environment.keys), Set(["HOME", "USER", "PATH", "TMPDIR", "CUA_DRIVER_EMBEDDED",
            "CUA_DRIVER_HOST_BUNDLE_ID", "CUA_DRIVER_RS_TELEMETRY_ENABLED"]))
        XCTAssertEqual(launch.environment["PATH"], "/usr/bin:/bin")
        XCTAssertEqual(launch.environment["CUA_DRIVER_RS_TELEMETRY_ENABLED"], "0")
        XCTAssertEqual(launch.environment["CUA_DRIVER_EMBEDDED"], "1")
        XCTAssertEqual(launch.environment["CUA_DRIVER_HOST_BUNDLE_ID"], "app.lina-os.helper")
        XCTAssertNil(launch.environment["CUA_DRIVER_POLICY_FILE"])
    }

    func testSuspendedFailureKillsWithoutResumeAndLatchesUntilExplicitRestart() throws {
        let f = fixture(); f.signatureOK = false; f.operate(); _ = f.supervisor.checkPolicy()
        XCTAssertTrue(f.events.contains("signal 101 \(SIGKILL)"))
        XCTAssertTrue(f.events.contains("reap 101"))
        XCTAssertFalse(f.events.contains("signal 101 \(SIGCONT)"))
        XCTAssertTrue(f.events.contains(where: { $0.hasPrefix("audit ") && $0.contains("signature") }))
        f.signatureOK = true; f.now = 100; f.grants.accessibility = true
        _ = f.supervisor.checkPolicy(); XCTAssertEqual(f.launches.count, 1)
        try f.supervisor.restart(); XCTAssertEqual(f.launches.count, 2)
    }

    func testProfileChangeClearsSignatureLatch() {
        let f = fixture(); f.signatureOK = false; f.operate(); _ = f.supervisor.checkPolicy()
        f.policy = .observe; _ = f.supervisor.checkPolicy()
        f.signatureOK = true; f.operate(); _ = f.supervisor.checkPolicy()
        XCTAssertEqual(f.launches.count, 2)
    }

    func testPermissionsSignatureFailureAlsoLatches() {
        let f = fixture(); f.permissionsSignatureOK = false; f.operate(); _ = f.supervisor.checkPolicy()
        XCTAssertEqual(f.supervisor.status()["running"], .bool(false))
        XCTAssertTrue(f.events.contains("signal 101 \(SIGKILL)"))
        f.now = 120; f.supervisor.poll(); XCTAssertEqual(f.launches.count, 1)
    }

    func testPeriodicProfileCheckAndConcurrentRequestsDoNotDuplicateChild() {
        let f = fixture(); _ = f.supervisor.checkPolicy()
        f.operate(); f.now = 4.9; f.supervisor.poll(); XCTAssertTrue(f.launches.isEmpty)
        f.now = 5; f.supervisor.poll(); XCTAssertEqual(f.launches.count, 1)
        DispatchQueue.concurrentPerform(iterations: 20) { _ in f.supervisor.checkPolicy() }
        XCTAssertEqual(f.launches.count, 1)
        f.policy = .observe; f.now = 10; f.supervisor.poll()
        XCTAssertTrue(f.events.contains("reap 101"))
    }

    func testRequestReconcilesPolicyBeforeDispatchUsingSocketpair() throws {
        let f = fixture()
        let config = Configuration(environment: ["LINA_HELPER_HOME": f.home, "LINA_HELPER_DEV": "1"], ownTeam: nil)
        let audit = try AuditLog(directory: config.auditDirectory)
        let server = HelperServer(configuration: config,
            dispatcher: ActionDispatcher(actions: ["status": Action { _ in [:] }]), audit: audit, cua: f.supervisor,
            own: .init(valid: true, team: nil), inspect: { _ in
                var peer = Peer(uid: getuid()); peer.identity = .init(valid: true, team: nil); return peer
            })
        for profile in ["operate", "observe"] {
            if profile == "operate" { f.operate() } else { f.policy = .observe }
            var fds = [Int32](repeating: -1, count: 2)
            XCTAssertEqual(socketpair(AF_UNIX, SOCK_STREAM, 0, &fds), 0)
            defer { close(fds[0]); close(fds[1]) }
            try Transport.prepare(fds[0]); try Transport.prepare(fds[1])
            try Transport.write(JSONEncoder().encode(Request(action: "status")) + Data([10]), to: fds[0])
            server.handle(fds[1])
            let response = try JSONDecoder().decode(Response.self, from: Transport.readLine(fds[0]))
            XCTAssertTrue(response.ok)
            XCTAssertEqual(response.result?["policy"]?.object?["profile"], .string(profile))
            XCTAssertEqual(f.supervisor.status()["running"], .bool(profile == "operate"))
        }
    }

    func testBackoffDoublesToSixtyAndResetsAfterFiveMinutes() {
        let f = fixture(); f.operate(); _ = f.supervisor.checkPolicy()
        for delay: Double in [1, 2, 4, 8, 16, 32, 60, 60] {
            let count = f.launches.count
            f.dead.insert(f.nextPID); f.supervisor.poll()
            f.now += delay - 0.01; f.supervisor.poll(); XCTAssertEqual(f.launches.count, count)
            f.now += 0.02; f.supervisor.poll(); XCTAssertEqual(f.launches.count, count + 1)
        }
        f.now += 300; f.dead.insert(f.nextPID); f.supervisor.poll()
        let count = f.launches.count
        f.now += 1; f.supervisor.poll(); XCTAssertEqual(f.launches.count, count + 1)
    }

    func testGrantChangesRestartAndReapBeforeNextSpawn() {
        let f = fixture(); f.operate(); _ = f.supervisor.checkPolicy()
        f.grants.screenRecording = true; _ = f.supervisor.checkPolicy()
        XCTAssertEqual(f.launches.count, 2)
        XCTAssertLessThan(f.events.firstIndex(of: "reap 101")!, f.events.firstIndex(of: "spawn 102")!)
        _ = f.supervisor.checkPolicy(); XCTAssertEqual(f.launches.count, 2)
        f.grants.accessibility = true; f.now += 5; f.supervisor.poll()
        XCTAssertEqual(f.launches.count, 3)
    }

    func testReadinessTimeoutAndStopDuringStartup() {
        let f = fixture(); f.ready = false; f.operate(); _ = f.supervisor.checkPolicy()
        XCTAssertEqual(f.supervisor.status()["running"], .bool(false))
        f.now = 10; f.supervisor.poll()
        XCTAssertTrue(f.events.contains("reap 101"))
        XCTAssertTrue(f.supervisor.status()["last_error"]?.string?.contains("socket") == true)
        f.now = 11; f.supervisor.poll()
        f.policy = .observe; _ = f.supervisor.checkPolicy()
        XCTAssertTrue(f.events.contains("reap 102"))
    }

    func testShutdownEscalatesAfterThreeSecondsAndCannotRestart() throws {
        let f = fixture(); f.operate(); _ = f.supervisor.checkPolicy(); f.ignoresTerm = true
        f.supervisor.shutdown()
        XCTAssertGreaterThanOrEqual(f.now, 3)
        XCTAssertLessThan(f.now, 3.1)
        XCTAssertTrue(f.events.contains("signal 101 \(SIGKILL)"))
        XCTAssertTrue(f.events.contains("reap 101"))
        _ = f.supervisor.checkPolicy(); f.supervisor.poll()
        XCTAssertThrowsError(try f.supervisor.restart())
        XCTAssertEqual(f.launches.count, 1)
    }

    func testUnusedSupervisorDoesNotRemoveAnotherHelpersFiles() throws {
        let f = fixture()
        try PrivateFiles.directory(f.home + "/run")
        let path = f.home + "/run/cua.sock"
        try Data("other helper's endpoint".utf8).write(to: URL(fileURLWithPath: path))
        // A second helper can fail to acquire helper.lock before monitoring begins.
        f.supervisor.shutdown()
        XCTAssertTrue(FileManager.default.fileExists(atPath: path))
    }

    func testLeaseActionsShareStatusAndValidateArguments() throws {
        let f = fixture()
        let dispatcher = ActionDispatcher(actions: SystemActions.registry(home: f.home, cua: f.supervisor, probe: { [:] }))
        let holder: [String: JSONValue] = ["holder": .string("hermes-owner")]
        for name in ["cua.lease", "cua.release"] {
            XCTAssertEqual(dispatcher.dispatch(Request(action: name, args: holder), policy: .observe).error?.code, .notAllowed)
        }
        f.operate(); _ = f.supervisor.checkPolicy()
        XCTAssertEqual(dispatcher.dispatch(Request(action: "cua.status"), policy: f.policy).result?["lease"], .null)
        let granted = dispatcher.dispatch(Request(action: "cua.lease", args: holder), policy: f.policy)
        XCTAssertTrue(granted.ok)
        XCTAssertEqual(granted.result, ["holder": .string("hermes-owner"), "expires_in_s": .number(300)])
        f.now = 10
        XCTAssertEqual(dispatcher.dispatch(Request(action: "cua.status"), policy: f.policy).result?["lease"],
                       .object(["holder": .string("hermes-owner"), "expires_in_s": .number(290)]))
        for name in ["cua.lease", "cua.release"] {
            let busy = dispatcher.dispatch(Request(action: name, args: ["holder": .string("hermes-other")]), policy: f.policy)
            XCTAssertEqual(busy.error?.code, .busy)
            XCTAssertEqual(busy.error?.detail, "hermes-owner is using this Mac (290s left)")
            for args: [String: JSONValue] in [[:], ["holder": .number(1)], ["holder": .null],
                ["holder": .string("has spaces")], holder.merging(["extra": .null]) { _, v in v }] {
                XCTAssertEqual(dispatcher.dispatch(Request(action: name, args: args), policy: f.policy).error?.code, .badRequest)
            }
        }
        for ttl: JSONValue in [.string("1"), .null, .bool(true), .number(0), .number(-1), .number(301)] {
            XCTAssertEqual(dispatcher.dispatch(Request(action: "cua.lease", args: holder.merging(["ttl_s": ttl]) { _, v in v }), policy: f.policy).error?.code, .badRequest)
        }
        let short = holder.merging(["ttl_s": JSONValue.number(1.5)]) { _, v in v }
        XCTAssertTrue(dispatcher.dispatch(Request(action: "cua.lease", args: short), policy: f.policy).ok)
        XCTAssertEqual(dispatcher.dispatch(Request(action: "cua.release", args: short), policy: f.policy).error?.code, .badRequest)
        try f.supervisor.restart()
        XCTAssertEqual(f.supervisor.status()["lease"]?.object?["holder"], .string("hermes-owner"))
        f.now += 1.5
        XCTAssertEqual(dispatcher.dispatch(Request(action: "cua.status"), policy: f.policy).result?["lease"], .null)
        XCTAssertTrue(dispatcher.dispatch(Request(action: "cua.lease", args: holder), policy: f.policy).ok)
        XCTAssertTrue(dispatcher.dispatch(Request(action: "cua.release", args: holder), policy: f.policy).ok)
        XCTAssertEqual(f.supervisor.status()["lease"], .null)
    }

    func testActionsAndProfiles() throws {
        let f = fixture()
        let dispatcher = ActionDispatcher(actions: SystemActions.registry(home: f.home, cua: f.supervisor, probe: { [:] }))
        XCTAssertTrue(Policy.observe.allows("cua.status"))
        XCTAssertFalse(Policy.observe.allows("cua.restart"))
        XCTAssertTrue(dispatcher.dispatch(Request(action: "cua.status"), policy: .observe).ok)
        XCTAssertFalse(dispatcher.dispatch(Request(action: "cua.restart"), policy: .observe).ok)
        f.operate(); _ = f.supervisor.checkPolicy()
        XCTAssertTrue(f.policy.allows("cua.restart"))
        let status = dispatcher.dispatch(Request(action: "cua.status"), policy: f.policy)
        XCTAssertEqual(status.result?["version"], .string(CuaDriverPin.version))
        XCTAssertEqual(status.result?["socket"], .string(f.home + "/run/cua.sock"))
        XCTAssertEqual(status.result?["gate_socket"], .string(f.home + "/run/cua.sock"))
        XCTAssertEqual(status.result?["daemon_socket"], .string(f.home + "/run/cua-daemon.sock"))
        XCTAssertEqual(status.result?["check_permissions"]?.object?["source"]?.object?["attribution"], .string("host"))
        XCTAssertTrue(dispatcher.dispatch(Request(action: "cua.restart"), policy: f.policy).ok)
        XCTAssertEqual(f.supervisor.status()["restarts"], .number(1))
        for action in ["cua.status", "cua.restart"] {
            XCTAssertFalse(dispatcher.dispatch(Request(action: action, args: ["x": .bool(true)]), policy: f.policy).ok)
        }
    }

    func testCompiledPinMatchesSinglePinFile() throws {
        let url = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("cua-driver.pin")
        let pin = try JSONDecoder().decode([String: String].self, from: Data(contentsOf: url))
        XCTAssertEqual(pin["version"], CuaDriverPin.version)
        XCTAssertEqual(pin["cdhash_arm64"], CuaDriverPin.cdhash)
        XCTAssertEqual(pin["team"], CuaDriverPin.team)
        XCTAssertEqual(pin["identifier"], CuaDriverPin.identifier)
        XCTAssertEqual(pin["cdhash_full_sha256_arm64"]?.prefix(40), CuaDriverPin.cdhash.prefix(40))
    }
}
