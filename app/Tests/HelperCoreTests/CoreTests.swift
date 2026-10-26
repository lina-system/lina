import XCTest
import Darwin
@testable import HelperCore

final class CoreTests: XCTestCase {
    func testPeerDecision() {
        let own = CodeIdentity(valid: true, team: "TESTTEAM")
        XCTAssertTrue(PeerAuthorization.allows(uid: 501, helperUID: 501, own: own,
                                             peer: own, development: false))
        XCTAssertTrue(PeerAuthorization.allows(uid: 0, helperUID: 501, own: own,
                                             peer: own, development: false))
        XCTAssertFalse(PeerAuthorization.allows(uid: 502, helperUID: 501, own: own,
                                              peer: own, development: true))
        XCTAssertFalse(PeerAuthorization.allows(uid: 501, helperUID: 501, own: own,
                                              peer: .init(valid: true, team: "OTHER"), development: true))
        XCTAssertFalse(PeerAuthorization.allows(uid: 501, helperUID: 501, own: own,
                                              peer: .init(valid: false, team: "TESTTEAM"), development: true))
        let adhoc = CodeIdentity(valid: true, team: nil)
        XCTAssertFalse(PeerAuthorization.allows(uid: 501, helperUID: 501, own: adhoc,
                                              peer: adhoc, development: false))
        XCTAssertTrue(PeerAuthorization.allows(uid: 501, helperUID: 501, own: adhoc,
                                             peer: adhoc, development: true))
        XCTAssertFalse(PeerAuthorization.allows(uid: 501, helperUID: 501,
                                              own: .init(valid: false, team: nil), peer: adhoc, development: true))
        XCTAssertFalse(PeerAuthorization.allows(uid: 501, helperUID: 501, own: adhoc,
                                              peer: .init(valid: false, team: nil), development: true))
    }

    func testProtocolValidationAndFraming() throws {
        let valid = Data("{\"v\":1,\"id\":\"test\",\"action\":\"status\",\"args\":{}}\n".utf8)
        var frame = LineFrame()
        XCTAssertNil(try frame.append(valid.prefix(8)))
        XCTAssertEqual(try Request.decode(try XCTUnwrap(frame.append(valid.dropFirst(8)))).action, "status")
        for raw in ["{}", "[]", "{\"v\":2,\"id\":\"x\",\"action\":\"status\"}",
                    "{\"v\":true,\"id\":\"x\",\"action\":\"status\"}",
                    "{\"v\":1,\"id\":\"\",\"action\":\"status\"}",
                    "{\"v\":1,\"id\":\"x\",\"action\":\"status\",\"args\":[]}"] {
            XCTAssertThrowsError(try Request.decode(Data(raw.utf8)), raw)
        }
        var large = LineFrame()
        XCTAssertThrowsError(try large.append(Data(repeating: 65, count: 65_537)))
        var boundary = LineFrame()
        XCTAssertNotNil(try boundary.append(Data(repeating: 32, count: 65_535) + Data([10])))
        var over = LineFrame()
        XCTAssertThrowsError(try over.append(Data(repeating: 32, count: 65_536) + Data([10])))
    }

    func testPolicyTrustAndFallback() throws {
        let raw = Data(#"{"actions":{"screenshot":{"max_bytes":1000},"cua.lease":{}}}"#.utf8)
        for (uid, mode, expected) in [(UInt32(0), UInt16(0o644), true),
                                      (501, 0o644, false), (0, 0o664, false), (0, 0o646, false)] {
            let policy = Policy.parse(raw, owner: uid, mode: mode)
            XCTAssertEqual(policy.allows("screenshot"), expected)
            XCTAssertTrue(policy.allows("status"))
            XCTAssertTrue(policy.allows("permissions"))
        }
        XCTAssertFalse(Policy.parse(Data("invalid".utf8), owner: 0, mode: 0o644).allows("screenshot"))
        XCTAssertFalse(Policy.load(path: "/nonexistent-lina-policy").allows("cua.lease"))
        let config = Configuration(environment: ["LINA_HELPER_HOME": "/tmp/lina-test",
                                                 "LINA_HELPER_POLICY": "/tmp/policy.json"], ownTeam: nil)
        XCTAssertEqual(config.socketPath, "/tmp/lina-test/run/helper.sock")
        XCTAssertEqual(config.policyPath, "/Library/Application Support/LINA Helper/policy.json")
        let dev = Configuration(environment: ["LINA_HELPER_DEV": "1", "LINA_HELPER_POLICY": "/tmp/policy.json"], ownTeam: nil)
        XCTAssertEqual(dev.policyPath, "/tmp/policy.json")
    }

    func testAuditRedactsRecursively() throws {
        let args: [String: JSONValue] = ["text": .string("abc"), "nested": .object([
            "contents": .string("private"), "pixels": .array([.number(1), .number(2)])])]
        let result = AuditLog.redact(args)
        XCTAssertEqual(result["text_len"], .number(3))
        XCTAssertEqual(result["text_sha256"], .string("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"))
        let encoded = String(decoding: try JSONEncoder().encode(result), as: UTF8.self)
        XCTAssertFalse(encoded.contains("private"))
        XCTAssertFalse(encoded.contains("abc"))
        XCTAssertFalse(encoded.contains("[1,2]"))
    }

    func testAuditCleanupAndMode() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        let old = directory.appendingPathComponent("audit-20200101.jsonl")
        let keep = directory.appendingPathComponent("unrelated.txt")
        try Data().write(to: old)
        try Data().write(to: keep)
        for url in [old, keep] {
            try FileManager.default.setAttributes([.modificationDate: now.addingTimeInterval(-31 * 86400)], ofItemAtPath: url.path)
        }
        let log = try AuditLog(directory: directory.path, now: now)
        try log.record(request: Request(action: "status"), peer: .init(uid: getuid()),
                       response: .success(id: "x", result: [:]), elapsed: 0, now: now)
        XCTAssertFalse(FileManager.default.fileExists(atPath: old.path))
        XCTAssertTrue(FileManager.default.fileExists(atPath: keep.path))
        let files = try FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)
            .filter { $0.lastPathComponent.hasPrefix("audit-") }
        XCTAssertEqual(files.count, 1)
        let attrs = try FileManager.default.attributesOfItem(atPath: XCTUnwrap(files.first).path)
        XCTAssertEqual((attrs[.posixPermissions] as? NSNumber)?.intValue, 0o600)
    }

    func testCLIParsing() throws {
        let options = try CLIOptions.parse(["status", "--json", #"{"display":"main"}"#,
                                            "--arg", "text=a=b", "--caller", "test", "--out", "-"],
                                           environment: ["SSH_CLIENT": "test-client"])
        XCTAssertEqual(options.request.args["text"], .string("a=b"))
        XCTAssertEqual(options.request.args["display"], .string("main"))
        XCTAssertEqual(options.request.caller, "test")
        XCTAssertEqual(options.request.sshClient, "test-client")
        XCTAssertNotNil(options.request.parentPID)
        XCTAssertEqual(options.output, "-")
        for args in [[], ["status", "--arg", "bad"], ["status", "--json", "[]"],
                     ["status", "--out"], ["status", "--unknown"]] {
            XCTAssertThrowsError(try CLIOptions.parse(args, environment: [:]))
        }
    }

    func testWireRequiresUTF8() throws {
        let text = #"{"v":1,"id":"x","action":"status"}"#
        XCTAssertThrowsError(try Request.decode(XCTUnwrap(text.data(using: .utf16))))
        XCTAssertEqual(try Request.decode(Data(text.utf8)).action, "status")
    }

    func testAuditDigestCannotBeOverwrittenByArgumentNames() {
        let redacted = AuditLog.redact(["text": .string("abc"), "text_len": .number(999)])
        XCTAssertEqual(redacted["text_len"], .number(3))
    }

    func testAuditDailyRetentionAndSymlinkRefusal() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let now = Calendar.current.startOfDay(for: Date())
        let log = try AuditLog(directory: directory.path, now: now)
        let formatter = DateFormatter(); formatter.dateFormat = "yyyyMMdd"
        let retainedDay = Calendar.current.date(byAdding: .day, value: -30, to: now)!
        let retained = directory.appendingPathComponent("audit-" + formatter.string(from: retainedDay) + ".jsonl")
        try Data().write(to: retained)
        try log.cleanup(now: now)
        XCTAssertTrue(FileManager.default.fileExists(atPath: retained.path))
        // The next day's write performs retention even without a manual cleanup.
        let tomorrow = Calendar.current.date(byAdding: .day, value: 1, to: now)!
        try log.record(request: nil, peer: .init(uid: getuid()), response: .success(id: "", result: [:]), elapsed: 0, now: tomorrow)
        XCTAssertFalse(FileManager.default.fileExists(atPath: retained.path))
        let target = directory.appendingPathComponent("unchanged")
        try Data("original".utf8).write(to: target)
        let today = directory.appendingPathComponent("audit-" + formatter.string(from: now) + ".jsonl")
        try FileManager.default.createSymbolicLink(at: today, withDestinationURL: target)
        XCTAssertThrowsError(try log.record(request: nil, peer: .init(uid: getuid()), response: .success(id: "", result: [:]), elapsed: 0, now: now))
        XCTAssertEqual(try Data(contentsOf: target), Data("original".utf8))
    }

    func testPolicyRejectsSymlinkAndMalformedActions() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let path = directory.appendingPathComponent("policy.json")
        let link = directory.appendingPathComponent("link.json")
        try Data(#"{"actions":{"cua.lease":{}}}"#.utf8).write(to: path)
        try FileManager.default.createSymbolicLink(at: link, withDestinationURL: path)
        XCTAssertFalse(Policy.load(path: link.path).allows("cua.lease"))
        XCTAssertFalse(Policy.parse(Data(#"{"actions":{"cua.lease":true}}"#.utf8), owner: 0, mode: 0o644).allows("cua.lease"))
    }
}
