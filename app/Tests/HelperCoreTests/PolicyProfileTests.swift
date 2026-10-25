import XCTest
import Darwin
@testable import HelperCore

final class PolicyProfileTests: XCTestCase {
    private func parse(_ raw: String, owner: uid_t = 0, mode: mode_t = 0o644) -> Policy {
        Policy.parse(Data(raw.utf8), owner: owner, mode: mode)
    }

    private func metadata(_ profile: String, _ source: String) -> JSONValue {
        .object(["profile": .string(profile), "source": .string(source)])
    }

    func testProfilesAndLimits() {
        let observe = parse(#"{"profile":"observe"}"#)
        let operate = parse(#"{"profile":"operate"}"#)
        for action in ["status", "permissions", "screenshot", "ax.tree"] {
            XCTAssertTrue(observe.allows(action), action)
            XCTAssertTrue(operate.allows(action), action)
        }
        for action in ["ax.press", "input.click", "input.keys", "app.open"] {
            XCTAssertFalse(observe.allows(action), action)
            XCTAssertTrue(operate.allows(action), action)
            XCTAssertEqual(operate.limits(for: action), [:])
        }
        for policy in [observe, operate] {
            XCTAssertFalse(policy.allows("unknown"))
            XCTAssertEqual(policy.limits(for: "screenshot"), ["timeout_s": .number(15)])
            XCTAssertEqual(policy.limits(for: "ax.tree"), ["max_depth": .number(8), "max_nodes": .number(2000)])
        }
    }

    func testMissingPolicyObservesButUnsafeOrInvalidRestricts() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let missing = dir.appendingPathComponent("missing.json")
        XCTAssertTrue(Policy.load(path: missing.path).allows("screenshot"))
        XCTAssertFalse(Policy.load(path: missing.path).allows("input.keys"))
        let file = dir.appendingPathComponent("policy.json")
        try Data(#"{"profile":"operate"}"#.utf8).write(to: file)
        // No privileged filesystem operations: the parser separately exercises root ownership.
        if geteuid() != 0 { XCTAssertFalse(Policy.load(path: file.path).allows("screenshot")) }
        XCTAssertFalse(Policy.load(path: dir.path).allows("screenshot"))
        let link = dir.appendingPathComponent("link")
        try FileManager.default.createSymbolicLink(at: link, withDestinationURL: missing)
        XCTAssertFalse(Policy.load(path: link.path).allows("screenshot"))
        let fifo = dir.appendingPathComponent("fifo")
        XCTAssertEqual(mkfifo(fifo.path, 0o600), 0)
        XCTAssertFalse(Policy.load(path: fifo.path).allows("screenshot"))
        XCTAssertFalse(parse(#"{"profile":"observe"}"#, owner: 501).allows("screenshot"))
        for mode: mode_t in [0o664, 0o646] {
            XCTAssertFalse(parse(#"{"profile":"observe"}"#, mode: mode).allows("screenshot"))
        }
        let oversized = Data((#"{"profile":"observe"}"# + String(repeating: " ", count: 1_048_576)).utf8)
        try oversized.write(to: file)
        XCTAssertFalse(Policy.load(path: file.path).allows("screenshot"))
        XCTAssertFalse(Policy.parse(oversized, owner: 0, mode: 0o644).allows("screenshot"))
        for raw in [#"{"profile":"unknown"}"#, #"{"profile":"observe","actions":{}}"#,
                    #"{"profile":null,"actions":{}}"#, #"{"profile":1}"#, "{}", "invalid"] {
            let policy = parse(raw)
            XCTAssertTrue(policy.allows("status"))
            XCTAssertTrue(policy.allows("permissions"))
            XCTAssertFalse(policy.allows("screenshot"), raw)
        }
        XCTAssertFalse(Policy.load(path: file.path + "/child").allows("screenshot")) // ENOTDIR, not ENOENT
    }

    func testStatusReportsThePolicyUsedForEachRequest() {
        let dispatcher = ActionDispatcher(actions: SystemActions.registry(home: "/nonexistent-lina-test", probe: { [:] }))
        let cases: [(Policy, JSONValue)] = [
            (Policy.load(path: "/nonexistent-lina-policy"), metadata("observe", "builtin")),
            (parse(#"{"profile":"observe"}"#), metadata("observe", "root")),
            (parse(#"{"profile":"operate"}"#), metadata("operate", "root")),
            (parse(#"{"actions":{"app.open":{}}}"#), metadata("custom", "root")),
            (.restricted, metadata("restricted", "restricted")),
        ]
        for (policy, expected) in cases {
            let response = dispatcher.dispatch(Request(action: "status"), policy: policy)
            XCTAssertTrue(response.ok)
            XCTAssertEqual(response.result?["policy"], expected)
        }
        let custom = parse(#"{"actions":{"app.open":{}}}"#)
        XCTAssertFalse(custom.allows("screenshot"))
        XCTAssertTrue(custom.allows("app.open"))
    }

    func testSignedConfigurationIgnoresDevelopmentOverrides() {
        let env = ["LINA_HELPER_DEV": "1", "LINA_HELPER_POLICY": "/tmp/test-policy.json"]
        let signed = Configuration(environment: env, ownTeam: "TESTTEAM01")
        XCTAssertFalse(signed.development)
        XCTAssertEqual(signed.policyPath, Configuration.defaultPolicy)
        for team: String? in [nil, ""] {
            let adhoc = Configuration(environment: env, ownTeam: team)
            XCTAssertTrue(adhoc.development)
            XCTAssertEqual(adhoc.policyPath, "/tmp/test-policy.json")
            XCTAssertEqual(Configuration(environment: ["LINA_HELPER_POLICY": "/tmp/test-policy.json"], ownTeam: team).policyPath,
                           Configuration.defaultPolicy)
        }
    }

    func testPolicyShortcutUsesStatusAndProjectsOnlyPolicy() throws {
        let options = try CLIOptions.parse(["policy", "--caller", "test"], environment: [:])
        XCTAssertEqual(options.request.action, "status")
        XCTAssertEqual(options.request.caller, "test")
        let result: [String: JSONValue] = ["policy": metadata("observe", "builtin"), "pid": .number(123)]
        let response = Response.success(id: options.request.id, result: result)
        let output = try JSONDecoder().decode([String: JSONValue].self, from: options.responseLine(response))
        XCTAssertEqual(output, ["policy": metadata("observe", "builtin")])
        let status = try CLIOptions.parse(["status"], environment: [:])
        // JSON object ordering is unspecified; compare the complete decoded payload.
        XCTAssertEqual(try JSONDecoder().decode(JSONValue.self, from: status.responseLine(response)),
                       try JSONDecoder().decode(JSONValue.self, from: response.line()))
        let failure = Response.failure(id: options.request.id, HelperError(.notAllowed, "test"))
        XCTAssertEqual(try JSONDecoder().decode(JSONValue.self, from: options.responseLine(failure)),
                       try JSONDecoder().decode(JSONValue.self, from: failure.line()))
        XCTAssertThrowsError(try CLIOptions.parse(["policy", "--out", "-"], environment: [:]))
    }

    func testCompiledProfilesMatchInstallerResource() throws {
        // The installer reads app/Resources/PolicyProfiles.json; the helper compiles the same table in.
        let url = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("Resources/PolicyProfiles.json")
        let decoded = try JSONDecoder().decode([String: [String: JSONValue]].self, from: Data(contentsOf: url))
        XCTAssertEqual(decoded, Policy.profiles)
    }
}
