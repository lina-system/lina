import XCTest
import Darwin
@testable import HelperCore

final class ServerTests: XCTestCase {
    private func withServer(authorized: Bool = true, capacity: Int = 16,
                            _ body: (Configuration, HelperServer) throws -> Void) throws {
        let home = "/tmp/lh-" + UUID().uuidString
        let config = Configuration(environment: ["LINA_HELPER_HOME": home, "LINA_HELPER_DEV": "1",
                                                  "LINA_HELPER_POLICY": home + "/missing-policy"])
        defer { try? FileManager.default.removeItem(atPath: home) }
        let log = try AuditLog(directory: config.auditDirectory)
        // Codex's outer sandbox can forbid bind even in writable directories.
        // Keep listener tests enabled on ordinary macOS/CI; transport/handler tests
        // below use socketpair and still run in that restricted environment.
        let probe = socket(AF_UNIX, SOCK_STREAM, 0)
        guard probe >= 0 else { throw HelperError(.internalError, "cannot create test socket") }
        let probePath = home + "/probe.sock"
        let bound = try Transport.withAddress(probePath) { Darwin.bind(probe, $0, $1) }
        let bindError = errno
        close(probe); unlink(probePath)
        if bound != 0 && bindError == EPERM { throw XCTSkip("environment prohibits Unix listener bind (EPERM)") }
        XCTAssertEqual(bound, 0)
        let dispatcher = ActionDispatcher(actions: ["status": Action { _ in ["test": .bool(true)] },
                                                     "permissions": Action { _ in [:] },
                                                     "cua.lease": Action { _ in [:] }])
        let server = HelperServer(configuration: config, dispatcher: dispatcher, audit: log,
                                  own: .init(valid: true, team: nil), capacity: capacity) { _ in
            var peer = Peer(uid: getuid())
            peer.identity = .init(valid: authorized, team: nil)
            return peer
        }
        try server.start()
        defer { server.stop() }
        try body(config, server)
    }

    func testRoundTripPolicyUnknownAndAudit() throws {
        try withServer { config, _ in
            let response = try Transport.exchange(Request(action: "status"), path: config.socketPath)
            XCTAssertTrue(response.ok)
            XCTAssertEqual(response.result?["test"], .bool(true))
            let denied = try Transport.exchange(Request(action: "cua.lease", args: ["holder": .string("sensitive sample")]), path: config.socketPath)
            XCTAssertEqual(denied.error?.code, .notAllowed)
            let unknown = try Transport.exchange(Request(action: "not.registered"), path: config.socketPath)
            XCTAssertEqual(unknown.error?.code, .unknownAction)
            let attrs = try FileManager.default.attributesOfItem(atPath: config.home + "/run")
            XCTAssertEqual((attrs[.posixPermissions] as? NSNumber)?.intValue, 0o700)
            let paths = try FileManager.default.contentsOfDirectory(atPath: config.auditDirectory)
            let data = try Data(contentsOf: URL(fileURLWithPath: config.auditDirectory + "/" + XCTUnwrap(paths.first)))
            let text = String(decoding: data, as: UTF8.self)
            XCTAssertEqual(text.split(separator: "\n").count, 3)
            XCTAssertFalse(text.contains("sensitive sample"))
            XCTAssertTrue(text.contains("holder_sha256"))
        }
    }

    func testUnauthorizedPeerIsRefusedAndAuditedBeforeReading() throws {
        try withServer(authorized: false) { config, _ in
            let fd = try Transport.connect(path: config.socketPath)
            defer { close(fd) }
            let response = try JSONDecoder().decode(Response.self, from: Transport.readLine(fd))
            XCTAssertEqual(response.error?.code, .notAllowed)
            let files = try FileManager.default.contentsOfDirectory(atPath: config.auditDirectory)
            XCTAssertEqual(files.count, 1)
            let data = try Data(contentsOf: URL(fileURLWithPath: config.auditDirectory + "/" + XCTUnwrap(files.first)))
            XCTAssertTrue(String(decoding: data, as: UTF8.self).contains("not_allowed"))
        }
    }

    func testFramingMalformedAndOneRequestPerConnection() throws {
        try withServer { config, _ in
            let fd = try Transport.connect(path: config.socketPath)
            defer { close(fd) }
            let request = Request(action: "status")
            let line = try JSONEncoder().encode(request) + Data([10])
            try Transport.write(line.prefix(5), to: fd)
            try Transport.write(line.dropFirst(5) + line, to: fd)
            XCTAssertTrue(try JSONDecoder().decode(Response.self, from: Transport.readLine(fd)).ok)
            XCTAssertThrowsError(try Transport.readLine(fd))
            for input in [Data("[]\n".utf8), Data([0xff, 10]), Data(repeating: 65, count: 65_536)] {
                let socket = try Transport.connect(path: config.socketPath)
                defer { close(socket) }
                try Transport.write(input, to: socket)
                let response = try JSONDecoder().decode(Response.self, from: Transport.readLine(socket))
                XCTAssertEqual(response.error?.code, .badRequest)
            }
        }
    }

    func testThreeSecondRequestTimeout() throws {
        try withServer { config, _ in
            let fd = try Transport.connect(path: config.socketPath)
            defer { close(fd) }
            try Transport.write(Data("{".utf8), to: fd)
            let started = ProcessInfo.processInfo.systemUptime
            let response = try JSONDecoder().decode(Response.self, from: Transport.readLine(fd, timeout: 5))
            let elapsed = ProcessInfo.processInfo.systemUptime - started
            XCTAssertEqual(response.error?.code, .timeout)
            XCTAssertGreaterThan(elapsed, 2.8)
            XCTAssertLessThan(elapsed, 4.5)
        }
    }

    func testSecondServerDoesNotUnlinkLiveSocket() throws {
        try withServer { config, _ in
            let other = HelperServer(configuration: config, dispatcher: ActionDispatcher(actions: [:]),
                                     audit: try AuditLog(directory: config.auditDirectory))
            XCTAssertThrowsError(try other.start())
            XCTAssertTrue(try Transport.exchange(Request(action: "status"), path: config.socketPath).ok)
        }
    }

    func testActionDeadlineRetainsCapacityAndLimitsReachHandler() throws {
        let release = DispatchSemaphore(value: 0)
        let finished = DispatchSemaphore(value: 0)
        let dispatcher = ActionDispatcher(actions: ["status": Action(timeout: 0.05) { _ in
            defer { finished.signal() }
            _ = release.wait(timeout: .now() + 2)
            return [:]
        }], capacity: 1)
        let first = dispatcher.dispatch(Request(action: "status"), policy: .restricted)
        XCTAssertEqual(first.error?.code, .timeout)
        XCTAssertEqual(dispatcher.dispatch(Request(action: "status"), policy: .restricted).error?.code, .busy)
        release.signal()
        XCTAssertEqual(finished.wait(timeout: .now() + 1), .success)

        let policy = Policy.parse(Data(#"{"actions":{"example":{"max_bytes":42}}}"#.utf8), owner: 0, mode: 0o644)
        let example = ActionDispatcher(actions: ["example": Action { $0.limits }])
        XCTAssertEqual(example.dispatch(Request(action: "example"), policy: policy).result?["max_bytes"], .number(42))
        XCTAssertEqual(example.dispatch(Request(action: "example"), policy: .restricted).error?.code, .notAllowed)
    }

    func testWriteDeadlineOnBlockedSocketAndNoSIGPIPE() throws {
        var pair: [Int32] = [0, 0]
        XCTAssertEqual(socketpair(AF_UNIX, SOCK_STREAM, 0, &pair), 0)
        defer { close(pair[0]); close(pair[1]) }
        try Transport.prepare(pair[0])
        var small: Int32 = 1024
        XCTAssertEqual(setsockopt(pair[0], SOL_SOCKET, SO_SNDBUF, &small, socklen_t(MemoryLayout<Int32>.size)), 0)
        XCTAssertThrowsError(try Transport.write(Data(repeating: 65, count: 1_048_576), to: pair[0], timeout: 0.05)) { error in
            XCTAssertEqual((error as? HelperError)?.code, .timeout)
        }
        shutdown(pair[1], SHUT_RDWR)
        XCTAssertThrowsError(try Transport.write(Data([1]), to: pair[0]))
    }

    func testSocketPathLengthValidation() {
        XCTAssertThrowsError(try Transport.address("relative/path"))
        XCTAssertThrowsError(try Transport.address("/" + String(repeating: "a", count: 104)))
        XCTAssertThrowsError(try Transport.address("/tmp/a\0b"))
    }
}
