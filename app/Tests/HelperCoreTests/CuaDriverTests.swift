import XCTest
import CryptoKit
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
    var wrongHash = false
    var querySignatureOK = true
    var permissionsSignatureOK = true
    var ignoresTerm = false
    var nextPID: pid_t = 100
    let home = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString).path
    var supervisor: CuaDriverSupervisor!
    init() {
        supervisor = CuaDriverSupervisor(home: home, executable: "/test/cua-driver", dependencies: CuaDependencies(
            policy: { [self] in policy }, grants: { [self] in grants }, now: { [self] in now },
            sleep: { [self] in now += $0 },
            spawn: { [self] launch in
                XCTAssertEqual(try Data(contentsOf: URL(fileURLWithPath: home + "/run/cua-policy.yaml")),
                               CuaPolicy.yaml(profile: "operate"))
                let attrs = try FileManager.default.attributesOfItem(atPath: home + "/run/cua-policy.yaml")
                XCTAssertEqual((attrs[.posixPermissions] as? NSNumber)?.intValue, 0o600)
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
            socketReady: { [self] _ in ready },
            query: { [self] launch in
                if !querySignatureOK { throw CuaSignatureError(detail: "query signature refused") }
                XCTAssertFalse(FileManager.default.fileExists(atPath: home + "/run/cua-policy.yaml"))
                XCTAssertNil(launch.environment["CUA_DRIVER_POLICY_FILE"])
                XCTAssertEqual(launch.environment["CUA_DRIVER_CLI_TELEMETRY_CHILD"], "1")
                if launch.arguments.first == "call" {
                    if !permissionsSignatureOK { throw CuaSignatureError(detail: "permission query signature refused") }
                    return #"{"source":{"attribution":"host"}}"#
                }
                return "  user policy sha256: " + (wrongHash ? String(repeating: "0", count: 64) :
                    CuaPolicy.digest(CuaPolicy.yaml(profile: "operate"))) + "\n"
            },
            audit: { [self] event in events.append("audit " + event) }
        ))
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
        for policy in [Policy.observe, .restricted, Policy.parse(Data(#"{"actions":{"input.keys":{}}}"#.utf8), owner: 0, mode: 0o644)] {
            f.policy = policy; _ = f.supervisor.checkPolicy(); XCTAssertTrue(f.launches.isEmpty)
        }
        f.operate(); _ = f.supervisor.checkPolicy()
        XCTAssertEqual(f.events.prefix(3), ["spawn 101", "verify 101", "signal 101 \(SIGCONT)"])
        XCTAssertEqual(f.supervisor.status()["running"], .bool(true))
        f.policy = .observe; _ = f.supervisor.checkPolicy()
        XCTAssertTrue(f.events.contains("signal 101 \(SIGTERM)"))
        XCTAssertTrue(f.events.contains("reap 101"))
        XCTAssertEqual(f.supervisor.status()["message"], .string("not running under observe"))
        XCTAssertFalse(FileManager.default.fileExists(atPath: f.home + "/run/cua-policy.yaml"))
    }

    func testLaunchContractAndNoInheritedConfiguration() {
        let f = fixture(); f.operate(); _ = f.supervisor.checkPolicy()
        let launch = f.launches[0]
        XCTAssertEqual(launch.arguments, ["serve", "--embedded", "--socket", f.home + "/run/cua.sock",
            "--no-permissions-gate", "--permission-mode", "standard", "--host-bundle-id", "app.lina-os.helper"])
        XCTAssertEqual(Set(launch.environment.keys), Set(["HOME", "USER", "PATH", "TMPDIR", "CUA_DRIVER_EMBEDDED",
            "CUA_DRIVER_HOST_BUNDLE_ID", "CUA_DRIVER_RS_TELEMETRY_ENABLED", "CUA_DRIVER_POLICY_FILE"]))
        XCTAssertEqual(launch.environment["PATH"], "/usr/bin:/bin")
        XCTAssertEqual(launch.environment["CUA_DRIVER_RS_TELEMETRY_ENABLED"], "0")
        XCTAssertEqual(launch.environment["CUA_DRIVER_EMBEDDED"], "1")
        XCTAssertEqual(launch.environment["CUA_DRIVER_HOST_BUNDLE_ID"], "app.lina-os.helper")
        XCTAssertEqual(launch.environment["CUA_DRIVER_POLICY_FILE"], f.home + "/run/cua-policy.yaml")
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

    func testQuerySignatureFailureLatchesAndKillsDaemon() {
        let f = fixture(); f.querySignatureOK = false; f.operate(); _ = f.supervisor.checkPolicy()
        XCTAssertEqual(f.supervisor.status()["running"], .bool(false))
        XCTAssertTrue(f.events.contains("signal 101 \(SIGKILL)"))
        f.now = 120; f.querySignatureOK = true; f.supervisor.poll()
        XCTAssertEqual(f.launches.count, 1)
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

    func testHashMismatchKillsAndDoesNotAdvertiseRunning() {
        let f = fixture(); f.wrongHash = true; f.operate(); _ = f.supervisor.checkPolicy()
        XCTAssertTrue(f.events.contains("signal 101 \(SIGKILL)"))
        XCTAssertEqual(f.supervisor.status()["running"], .bool(false))
        XCTAssertTrue(f.supervisor.status()["last_error"]?.string?.contains("policy hash") == true)
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
        XCTAssertFalse(FileManager.default.fileExists(atPath: f.home + "/run/cua-policy.yaml"))
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
        XCTAssertEqual(status.result?["check_permissions"]?.object?["source"]?.object?["attribution"], .string("host"))
        XCTAssertTrue(dispatcher.dispatch(Request(action: "cua.restart"), policy: f.policy).ok)
        XCTAssertEqual(f.supervisor.status()["restarts"], .number(1))
        for action in ["cua.status", "cua.restart"] {
            XCTAssertFalse(dispatcher.dispatch(Request(action: action, args: ["x": .bool(true)]), policy: f.policy).ok)
        }
    }

    func testYAMLIsDenyByDefaultWithExactlyFleetAllowlist() {
        let expected = "get_window_state get_desktop_state get_accessibility_tree list_apps list_windows get_screen_size get_cursor_position zoom check_permissions health_report get_guide start_session end_session get_session list_sessions click double_click right_click drag scroll move_cursor type_text press_key hotkey set_value invoke_menu launch_app bring_to_front run_steps verify_state".split(separator: " ").map(String.init)
        let yaml = String(decoding: CuaPolicy.yaml(profile: "operate"), as: UTF8.self)
        XCTAssertEqual(yaml, "allow:\n  tools:\n" + expected.map { "    - \($0)\n" }.joined())
        for profile in ["observe", "restricted", "custom", "unknown"] {
            XCTAssertEqual(String(decoding: CuaPolicy.yaml(profile: profile), as: UTF8.self), "allow: []\n")
        }
    }

    func testPolicyHashUsesUpstreamFramingAndStatusParsingFailsClosed() {
        XCTAssertEqual(CuaPolicy.digest(Data("allow: []\n".utf8)), "2e874fb3efeb6e2e0de676ed7823ee096af61d45774eccc7007db8cd523d9070")
        let hash = String(repeating: "a", count: 64)
        XCTAssertEqual(CuaPolicy.statusDigest("Daemon:\n  user policy sha256: \(hash)\n"), hash)
        for text in ["", "user policy sha256: none", "managed policy sha256: \(hash)",
                     "user policy sha256: \(hash)junk", "user policy sha256: \(hash)\nuser policy sha256: \(hash)"] {
            XCTAssertNil(CuaPolicy.statusDigest(text))
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
