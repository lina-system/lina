import XCTest
import Darwin
@testable import HelperCore

/// Exercise the production connection handler over real kernel stream sockets
/// even when the enclosing process sandbox forbids creating named listeners.
final class ConnectionTests: XCTestCase {
    private func connection(authorized: Bool = true, _ body: (Int32, String) throws -> Void) throws {
        let home = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString).path
        defer { try? FileManager.default.removeItem(atPath: home) }
        let config = Configuration(environment: ["LINA_HELPER_HOME": home, "LINA_HELPER_DEV": "1",
                                                  "LINA_HELPER_POLICY": home + "/missing.json"])
        let log = try AuditLog(directory: config.auditDirectory)
        let dispatcher = ActionDispatcher(actions: ["status": Action { _ in ["test": .bool(true)] },
                                                     "cua.lease": Action { _ in [:] }])
        let server = HelperServer(configuration: config, dispatcher: dispatcher, audit: log, outbox: Outbox(home: home),
                                  own: .init(valid: true, team: nil)) { _ in
            var peer = Peer(uid: getuid()); peer.identity = .init(valid: authorized, team: nil)
            return peer
        }
        var fds: [Int32] = [0, 0]
        guard socketpair(AF_UNIX, SOCK_STREAM, 0, &fds) == 0 else { return XCTFail("socketpair failed") }
        let clientFD = fds[0], serverFD = fds[1]
        let complete = DispatchSemaphore(value: 0)
        defer { close(clientFD); XCTAssertEqual(complete.wait(timeout: .now() + 5), .success) }
        try Transport.prepare(clientFD); try Transport.prepare(serverFD)
        DispatchQueue.global().async {
            defer { close(serverFD); complete.signal() }
            server.handle(serverFD)
        }
        try body(clientFD, config.auditDirectory)
    }

    func testSplitRequestOneResponseAndAttribution() throws {
        try connection { fd, directory in
            let request = Request(action: "status", caller: "test", sshClient: "test-client", parentPID: 42)
            let line = try JSONEncoder().encode(request) + Data([10])
            try Transport.write(line.prefix(4), to: fd)
            try Transport.write(line.dropFirst(4) + line, to: fd)
            let response = try JSONDecoder().decode(Response.self, from: Transport.readLine(fd))
            XCTAssertTrue(response.ok)
            XCTAssertEqual(response.id, request.id)
            XCTAssertThrowsError(try Transport.readLine(fd))
            let file = try XCTUnwrap(FileManager.default.contentsOfDirectory(atPath: directory).first)
            let data = try Data(contentsOf: URL(fileURLWithPath: directory + "/" + file))
            let entry = try JSONDecoder().decode([String: JSONValue].self, from: data)
            XCTAssertEqual(entry["caller"], .string("test"))
            XCTAssertEqual(entry["peer"]?.object?["ssh"], .string("test-client"))
            XCTAssertEqual(entry["peer"]?.object?["parent_pid"], .number(42))
        }
    }

    func testEveryRequestCleansOutboxEvenWhenPolicyDeniesAction() throws {
        for action in ["status", "cua.lease", "unknown"] {
            try connection { fd, directory in
                let home = (directory as NSString).deletingLastPathComponent
                let outbox = Outbox(home: home)
                let expired = try outbox.writePNG(Data([1]))
                try FileManager.default.setAttributes([.modificationDate: Date().addingTimeInterval(-601)], ofItemAtPath: expired)
                try Transport.write(JSONEncoder().encode(Request(action: action)) + Data([10]), to: fd)
                _ = try Transport.readLine(fd)
                XCTAssertFalse(FileManager.default.fileExists(atPath: expired))
            }
        }
    }

    func testDeniedPeerWithoutInput() throws {
        try connection(authorized: false) { fd, directory in
            let response = try JSONDecoder().decode(Response.self, from: Transport.readLine(fd))
            XCTAssertEqual(response.error?.code, .notAllowed)
            XCTAssertEqual(try FileManager.default.contentsOfDirectory(atPath: directory).count, 1)
        }
    }

    func testMalformedOversizedTruncatedRequests() throws {
        for input in [Data("[]\n".utf8), Data([0xff, 10]), Data(repeating: 65, count: 65_536), Data("{".utf8)] {
            try connection { fd, _ in
                try Transport.write(input, to: fd)
                shutdown(fd, SHUT_WR)
                let response = try JSONDecoder().decode(Response.self, from: Transport.readLine(fd))
                XCTAssertEqual(response.error?.code, .badRequest)
            }
        }
    }

    func testPolicyAndUnknownActionErrors() throws {
        for (action, code) in [("cua.lease", ErrorCode.notAllowed), ("absent", .unknownAction)] {
            try connection { fd, _ in
                try Transport.write(JSONEncoder().encode(Request(action: action)) + Data([10]), to: fd)
                let response = try JSONDecoder().decode(Response.self, from: Transport.readLine(fd))
                XCTAssertEqual(response.error?.code, code)
            }
        }
    }

    func testPartialRequestHasThreeSecondTotalDeadline() throws {
        try connection { fd, _ in
            let start = ProcessInfo.processInfo.systemUptime
            try Transport.write(Data("{".utf8), to: fd)
            // More bytes must not reset the whole-frame deadline.
            Thread.sleep(forTimeInterval: 1)
            try Transport.write(Data("\"".utf8), to: fd)
            let response = try JSONDecoder().decode(Response.self, from: Transport.readLine(fd, timeout: 4))
            XCTAssertEqual(response.error?.code, .timeout)
            let elapsed = ProcessInfo.processInfo.systemUptime - start
            XCTAssertGreaterThan(elapsed, 2.8)
            XCTAssertLessThan(elapsed, 3.8)
        }
    }
}
