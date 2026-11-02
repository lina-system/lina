import XCTest
import Darwin
@testable import HelperCore

private final class GateDaemon: @unchecked Sendable {
    private let lock = NSLock()
    private var received: [Data] = []
    var requests: [Data] { lock.lock(); defer { lock.unlock() }; return received }
    let reply: Data
    let respond: (@Sendable (Int32) throws -> Void)?
    let beforeRequest: @Sendable () throws -> Void
    init(reply: Data = Data(), beforeRequest: @escaping @Sendable () throws -> Void = {},
         respond: (@Sendable (Int32) throws -> Void)? = nil) {
        self.reply = reply; self.beforeRequest = beforeRequest; self.respond = respond
    }
    func serve(_ fd: Int32) {
        defer { close(fd) }
        do {
            try Transport.prepare(fd)
            let line = try Transport.readLine(fd, maximumBytes: 4 * 1024 * 1024) + Data([10])
            try beforeRequest()
            lock.lock(); received.append(line); lock.unlock()
            if let respond { try respond(fd) }
            else { try Transport.write(reply, to: fd, timeout: 5) }
        } catch { XCTFail("fake daemon: \(error)") }
    }
    func connect() throws -> Int32 {
        var fds: [Int32] = [-1, -1]
        guard socketpair(AF_UNIX, SOCK_STREAM, 0, &fds) == 0 else { throw HelperError(.internalError, "socketpair") }
        try Transport.prepare(fds[0])
        let daemonFD = fds[1]
        DispatchQueue.global().async { self.serve(daemonFD) }
        return fds[0]
    }
}

final class CuaGateTests: XCTestCase {
    private let own = CodeIdentity(valid: true, team: "HELPER")
    private func peer() -> Peer {
        var peer = Peer(uid: getuid())
        peer.pid = 123; peer.executable = "/test/proxy"
        peer.parentPID = 42; peer.parentExecutable = "/test/hermes"
        peer.identity = .init(valid: true, team: "YCK386LBJ7", identifier: "cua-driver")
        return peer
    }
    private func home() throws -> String {
        let path = "/tmp/cg-" + UUID().uuidString
        try PrivateFiles.directory(path)
        addTeardownBlock { try? FileManager.default.removeItem(atPath: path) }
        return path
    }
    private func entries(_ home: String) throws -> [[String: JSONValue]] {
        try Self.readEntries(home)
    }
    private static func readEntries(_ home: String) throws -> [[String: JSONValue]] {
        let files = try FileManager.default.contentsOfDirectory(atPath: home + "/logs")
        return try files.flatMap { file in
            try Data(contentsOf: URL(fileURLWithPath: home + "/logs/" + file)).split(separator: 10).map {
                try JSONDecoder().decode([String: JSONValue].self, from: Data($0))
            }
        }
    }
    private func pair(_ gate: CuaGate, body: (Int32) throws -> Void) throws {
        var fds: [Int32] = [-1, -1]
        XCTAssertEqual(socketpair(AF_UNIX, SOCK_STREAM, 0, &fds), 0)
        let client = fds[0], server = fds[1]
        try Transport.prepare(client); try Transport.prepare(server)
        let done = DispatchSemaphore(value: 0)
        DispatchQueue.global().async { gate.handle(server); done.signal() }
        defer {
            shutdown(client, SHUT_RDWR); close(client)
            XCTAssertEqual(done.wait(timeout: .now() + 3), .success)
        }
        try body(client)
    }
    private func readAll(_ fd: Int32) throws -> Data {
        var result = Data(), buffer = [UInt8](repeating: 0, count: 32 * 1024)
        while true {
            try Transport.wait(fd, events: Int16(POLLIN), deadline: ProcessInfo.processInfo.systemUptime + 5)
            let n = recv(fd, &buffer, buffer.count, 0)
            if n < 0 && (errno == EINTR || errno == EAGAIN) { continue }
            guard n >= 0 else { throw HelperError(.internalError, "test read failed") }
            if n == 0 { return result }
            result.append(contentsOf: buffer.prefix(n))
        }
    }
    private func bindListener(_ path: String) throws -> Int32 {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        let result = try Transport.withAddress(path) { Darwin.bind(fd, $0, $1) }
        let code = errno
        if result != 0 {
            close(fd)
            if code == EPERM { throw XCTSkip("environment prohibits Unix listener bind (EPERM)") }
            throw HelperError(.internalError, "test bind failed: \(code)")
        }
        XCTAssertEqual(listen(fd, 16), 0)
        try Transport.prepare(fd)
        return fd
    }

    private func exchange(_ gate: CuaGate, _ request: [String: JSONValue]) throws -> [String: JSONValue] {
        var response: [String: JSONValue] = [:]
        try pair(gate) { fd in
            try Transport.write(WireJSON.encode(request), to: fd)
            response = try JSONDecoder().decode([String: JSONValue].self, from: readAll(fd))
        }
        return response
    }

    func testLeaseBlocksEveryInputAndAuditsRefusalButAllowsReads() throws {
        let home = try home(), peer = peer()
        let lease = CuaInputLease(now: { 0 })
        _ = try lease.grant(holder: "hermes-owner")
        let daemon = GateDaemon(reply: Data("{\"ok\":true}\n".utf8))
        let gate = CuaGate(home: home, audit: try AuditLog(directory: home + "/logs"), lease: lease,
                           own: own, inspect: { _ in peer }, connect: { _ in try daemon.connect() })
        let inputs = "click double_click right_click drag scroll move_cursor type_text type_text_chars press_key hotkey set_value invoke_menu launch_app bring_to_front kill_app run_steps run_script set_window_frame clipboard_write browser_click browser_type browser_navigate browser_pointer browser_select browser_set_input_files browser_dialog browser_download page replay_trajectory".split(separator: " ")
        let args: JSONValue = .object(["session": .string("hermes-other"), "text": .string("secret")])
        for name in inputs {
            let result = try exchange(gate, ["method": .string("call"), "name": .string(String(name)), "args": args])
            XCTAssertEqual(result, ["ok": .bool(false), "error": .string("hermes-owner is using this Mac (300s left)"), "exit_code": .number(75)], String(name))
        }
        XCTAssertTrue(daemon.requests.isEmpty)
        let records = try entries(home)
        XCTAssertEqual(records.count, inputs.count)
        for (record, name) in zip(records, inputs) {
            XCTAssertEqual(record["action"], .string("cua.call:" + name))
            XCTAssertEqual(record["ok"], .bool(false))
            XCTAssertEqual(record["error"], .string("lease held by hermes-owner"))
            XCTAssertEqual(record["args"], .object(AuditLog.redact(args.object!)))
        }
        // Unknown/new tools and all non-call methods remain open under operate.
        for request: [String: JSONValue] in [
            ["method": .string("call"), "name": .string("get_window_state"), "args": args],
            ["method": .string("call"), "name": .string("clipboard_read")],
            ["method": .string("call"), "name": .string("future_tool")],
            ["method": .string("list"), "name": .string("click")],
            ["method": .string("session_begin")]] {
            XCTAssertEqual(try exchange(gate, request)["ok"], .bool(true))
        }
        XCTAssertEqual(daemon.requests.count, 5)
    }

    func testNoLeaseOwnLeaseExpiryAndInputDoesNotExtend() throws {
        let home = try home(), peer = peer(), clock = Locked<TimeInterval>(0)
        let lease = CuaInputLease(now: { clock.withLock { $0 } })
        let daemon = GateDaemon(reply: Data("{\"ok\":true}\n".utf8))
        let gate = CuaGate(home: home, audit: try AuditLog(directory: home + "/logs"), lease: lease,
                           own: own, inspect: { _ in peer }, connect: { _ in try daemon.connect() })
        let anonymous: [String: JSONValue] = ["method": .string("call"), "name": .string("click")]
        XCTAssertEqual(try exchange(gate, anonymous)["ok"], .bool(true))
        _ = try lease.grant(holder: "hermes-owner", ttl: 10)
        XCTAssertEqual(try exchange(gate, anonymous)["exit_code"], .number(75))
        // A top-level session_id is not the lease identity.
        XCTAssertEqual(try exchange(gate, anonymous.merging(["session_id": .string("hermes-owner")]) { _, v in v })["exit_code"], .number(75))
        clock.withLock { $0 = 9 }
        var ownRequest = anonymous
        ownRequest["args"] = .object(["session": .string("hermes-owner")])
        XCTAssertEqual(try exchange(gate, ownRequest)["ok"], .bool(true))
        XCTAssertEqual(lease.status().object?["expires_in_s"], .number(1))
        clock.withLock { $0 = 10 }
        var other = anonymous
        other["args"] = .object(["session": .string("hermes-other")])
        XCTAssertEqual(try exchange(gate, other)["ok"], .bool(true))
        XCTAssertEqual(lease.status(), .null)
        XCTAssertEqual(daemon.requests.count, 3)
    }

    func testIdentityFallbackIsPerProxyPIDAndExpiresAfterTenMinutes() throws {
        let home = try home(), clock = Locked<TimeInterval>(0), currentPeer = Locked(peer())
        let lease = CuaInputLease(now: { clock.withLock { $0 } })
        let daemon = GateDaemon(reply: Data("{\"ok\":true}\n".utf8))
        let gate = CuaGate(home: home, audit: try AuditLog(directory: home + "/logs"), lease: lease,
                           own: own, now: { clock.withLock { $0 } }, inspect: { _ in currentPeer.withLock { $0 } },
                           connect: { _ in try daemon.connect() })
        let input: [String: JSONValue] = ["method": .string("call"), "name": .string("bring_to_front")]
        let read: [String: JSONValue] = ["method": .string("call"), "name": .string("list_apps"),
                                        "args": .object(["session": .string("hermes-owner")])]
        XCTAssertEqual(try exchange(gate, read)["ok"], .bool(true))
        clock.withLock { $0 = 500 }
        _ = try lease.grant(holder: "hermes-owner")
        XCTAssertEqual(try exchange(gate, input)["ok"], .bool(true))
        for session: JSONValue in [.string(""), .null, .number(1)] {
            XCTAssertEqual(try exchange(gate, input.merging(["args": .object(["session": session])]) { _, v in v })["ok"], .bool(true))
        }
        // Same parent executable and PID, different proxy PID: no shared identity.
        currentPeer.withLock { $0.pid = 124 }
        XCTAssertEqual(try exchange(gate, input)["exit_code"], .number(75))
        currentPeer.withLock { $0.pid = nil }
        XCTAssertEqual(try exchange(gate, read)["ok"], .bool(true))
        XCTAssertEqual(try exchange(gate, input)["exit_code"], .number(75))
        currentPeer.withLock { $0.pid = 123 }
        clock.withLock { $0 = 599.9 }
        XCTAssertEqual(try exchange(gate, input)["ok"], .bool(true))
        // Falling back never refreshes the last explicit session's timestamp.
        clock.withLock { $0 = 600.1 }
        XCTAssertEqual(try exchange(gate, input)["exit_code"], .number(75))
        XCTAssertEqual(try exchange(gate, read)["ok"], .bool(true))
        XCTAssertEqual(try exchange(gate, input)["ok"], .bool(true))
        var other = input
        other["args"] = .object(["session": .string("hermes-other")])
        XCTAssertEqual(try exchange(gate, other)["exit_code"], .number(75))
        // The last explicit identity wins, even when that request was refused.
        XCTAssertEqual(try exchange(gate, input)["exit_code"], .number(75))
    }

    func testElementIndexDroppedOnlyWhenTokenPresent() throws {
        let both = try JSONDecoder().decode([String: JSONValue].self, from: Data(
            #"{"method":"call","name":"click","args":{"pid":1,"element_index":3,"element_token":"t3"},"session_id":"s"}"#.utf8))
        let adapted = try XCTUnwrap(CuaGate.adaptForDriver(both))
        XCTAssertEqual(adapted["args"]?.object?["element_index"], nil)
        XCTAssertEqual(adapted["args"]?.object?["element_token"], .string("t3"))
        XCTAssertEqual(adapted["session_id"], .string("s"))
        let indexOnly = try JSONDecoder().decode([String: JSONValue].self, from: Data(
            #"{"method":"call","name":"click","args":{"element_index":3}}"#.utf8))
        XCTAssertNil(CuaGate.adaptForDriver(indexOnly))
        let notCall = try JSONDecoder().decode([String: JSONValue].self, from: Data(
            #"{"method":"list","args":{"element_index":3,"element_token":"t"}}"#.utf8))
        XCTAssertNil(CuaGate.adaptForDriver(notCall))
        let gws = try JSONDecoder().decode([String: JSONValue].self, from: Data(
            #"{"method":"call","name":"get_window_state","args":{"pid":1,"window_id":2}}"#.utf8))
        XCTAssertEqual(CuaGate.adaptForDriver(gws)?["args"]?.object?["tree_format"], .string("both"))
        let gwsExplicit = try JSONDecoder().decode([String: JSONValue].self, from: Data(
            #"{"method":"call","name":"get_window_state","args":{"tree_format":"markdown"}}"#.utf8))
        XCTAssertNil(CuaGate.adaptForDriver(gwsExplicit))
    }

    func testAuthorizationMatrix() {
        let upstream = CodeIdentity(valid: true, team: "YCK386LBJ7", identifier: "cua-driver")
        let adhoc = CodeIdentity(valid: true, team: nil)
        for dev in [false, true] {
            XCTAssertTrue(PeerAuthorization.allowsCua(uid: 501, helperUID: 501, own: own, peer: upstream, development: dev))
            XCTAssertTrue(PeerAuthorization.allowsCua(uid: 501, helperUID: 501, own: own, peer: own, development: dev))
            for denied in [adhoc, .init(valid: false, team: "HELPER"),
                           .init(valid: true, team: "OTHER", identifier: "cua-driver"),
                           .init(valid: true, team: "YCK386LBJ7", identifier: "other")] {
                XCTAssertFalse(PeerAuthorization.allowsCua(uid: 501, helperUID: 501, own: own, peer: denied, development: dev))
            }
            for uid: uid_t in [0, 502] {
                XCTAssertFalse(PeerAuthorization.allowsCua(uid: uid, helperUID: 501, own: own, peer: upstream, development: dev))
            }
        }
        XCTAssertFalse(PeerAuthorization.allowsCua(uid: 501, helperUID: 501, own: adhoc, peer: adhoc, development: false))
        XCTAssertTrue(PeerAuthorization.allowsCua(uid: 501, helperUID: 501, own: adhoc, peer: adhoc, development: true))
        XCTAssertFalse(PeerAuthorization.allowsCua(uid: 501, helperUID: 501, own: .init(valid: false, team: nil), peer: upstream, development: true))
    }

    func testFiveMegabyteReplyByteExactAndAuditBeforeDaemon() throws {
        let home = try home(), peer = peer()
        let reply = Data("{\"png\":\"".utf8) + Data(repeating: 65, count: 5 * 1024 * 1024) + Data("\"}\n".utf8)
        let daemon = GateDaemon(reply: reply, beforeRequest: {
            XCTAssertEqual(try Self.readEntries(home).count, 1, "audit must precede forwarding")
        })
        let gate = CuaGate(home: home, audit: try AuditLog(directory: home + "/logs"), own: own,
                           inspect: { _ in peer }, connect: { _ in try daemon.connect() })
        let request = Data(#"{ "method":"call", "name":"type_text", "args":{"text":"private typed text","x":23},"session_id":"s-1" }"#.utf8) + Data([10])
        try pair(gate) { fd in
            try Transport.write(request.prefix(7), to: fd)
            try Transport.write(request.dropFirst(7), to: fd)
            XCTAssertEqual(try readAll(fd), reply)
        }
        XCTAssertEqual(daemon.requests, [request])
        let records = try entries(home), entry = try XCTUnwrap(records.first)
        XCTAssertEqual(records.count, 1)
        XCTAssertNotNil(UUID(uuidString: try XCTUnwrap(entry["id"]?.string)))
        XCTAssertNotNil(entry["t"]?.string)
        XCTAssertEqual(entry["action"], .string("cua.call:type_text"))
        XCTAssertEqual(entry["ok"], .bool(true))
        XCTAssertNil(entry["error"])
        XCTAssertEqual(entry["session_id"], .string("s-1"))
        XCTAssertEqual(entry["args"], .object(AuditLog.redact(["text": .string("private typed text"), "x": .number(23)])))
        XCTAssertEqual(entry["peer"], .object(["uid": .number(Double(getuid())), "pid": .number(123),
            "exe": .string("/test/proxy"), "team": .string("YCK386LBJ7"),
            "parent_pid": .number(42), "parent_exe": .string("/test/hermes")]))
        XCTAssertFalse(String(decoding: try JSONEncoder().encode(records), as: UTF8.self).contains("private typed text"))
    }

    func testPipelinedLinesEachHaveAuditAndFreshDaemonConnection() throws {
        let home = try home(), peer = peer(), reply = Data("{\"ok\":true}\n".utf8)
        let daemon = GateDaemon(reply: reply)
        let gate = CuaGate(home: home, audit: try AuditLog(directory: home + "/logs"), own: own,
                           inspect: { _ in peer }, connect: { _ in try daemon.connect() })
        let requests = [#"{"method":"list"}"#, #"{"method":"metadata"}"#,
                        #"{"method":"session_begin","session_id":"abc"}"#,
                        #"{"method":"call","name":"click","args":{"x":10}}"#,
                        #"{"method":"future_opaque","anything":[1,2]}"#, #"{"method":"session_end"}"#]
            .map { Data(($0 + "\n").utf8) }
        try pair(gate) { fd in
            try Transport.write(requests.reduce(Data(), +), to: fd)
            XCTAssertEqual(try readAll(fd), requests.reduce(Data()) { result, _ in result + reply })
        }
        XCTAssertEqual(daemon.requests, requests)
        let records = try entries(home)
        XCTAssertEqual(records.map { $0["action"]?.string }, ["cua.list", "cua.metadata", "cua.session_begin", "cua.call:click", "cua.future_opaque", "cua.session_end"])
        XCTAssertEqual(Set(records.compactMap { $0["id"]?.string }).count, requests.count)
        XCTAssertTrue(records.allSatisfy { $0["ok"] == .bool(true) })
    }

    func testRefusedPeerNeverConnectsAndIsAuditedWithoutReading() throws {
        let home = try home()
        let gate = CuaGate(home: home, audit: try AuditLog(directory: home + "/logs"), own: own,
            inspect: { _ in Peer(uid: getuid()) }, connect: { _ in XCTFail("refused peer connected"); return -1 })
        try pair(gate) { fd in
            let response = try JSONDecoder().decode([String: JSONValue].self, from: Transport.readLine(fd))
            XCTAssertEqual(response, ["ok": .bool(false), "error": .string("peer identity refused")])
        }
        let records = try entries(home)
        XCTAssertEqual(records.count, 1)
        XCTAssertEqual(records.first?["ok"], .bool(false))
        XCTAssertEqual(records.first?["error"], .string("peer identity refused"))
    }

    func testAuditFailureBlocksForwarding() throws {
        let home = try home(), peer = peer()
        let audit = try AuditLog(directory: home + "/logs")
        try FileManager.default.removeItem(atPath: home + "/logs")
        let gate = CuaGate(home: home, audit: audit, own: own, inspect: { _ in peer },
                           connect: { _ in XCTFail("unaudited request connected"); return -1 })
        try pair(gate) { fd in
            try Transport.write(Data("{\"method\":\"call\",\"name\":\"click\"}\n".utf8), to: fd)
            let response = try JSONDecoder().decode([String: JSONValue].self, from: Transport.readLine(fd))
            XCTAssertEqual(response, ["ok": .bool(false), "error": .string("audit unavailable")])
        }
    }

    func testInvalidAndOversizedLinesAreAuditedAndNeverForwarded() throws {
        for data in [Data("[]\n".utf8), Data("null\n".utf8), Data("no json\n".utf8), Data([0xff, 10]),
                     Data(repeating: 65, count: 4 * 1024 * 1024)] {
            let home = try home(), peer = peer()
            let gate = CuaGate(home: home, audit: try AuditLog(directory: home + "/logs"), own: own,
                inspect: { _ in peer }, connect: { _ in XCTFail("invalid request connected"); return -1 })
            try pair(gate) { fd in
                try Transport.write(data, to: fd)
                let response = try JSONDecoder().decode([String: JSONValue].self, from: Transport.readLine(fd))
                XCTAssertEqual(response["ok"], .bool(false))
            }
            XCTAssertEqual(try entries(home).count, 1)
            XCTAssertEqual(try entries(home).first?["ok"], .bool(false))
        }
    }

    func testRequestReadTimeoutIsAudited() throws {
        let home = try home(), peer = peer()
        let gate = CuaGate(home: home, audit: try AuditLog(directory: home + "/logs"), own: own,
                           readTimeout: 0.05, inspect: { _ in peer })
        try pair(gate) { fd in
            try Transport.write(Data("{".utf8), to: fd)
            let response = try JSONDecoder().decode([String: JSONValue].self, from: Transport.readLine(fd))
            XCTAssertEqual(response["error"], .string("request timeout"))
        }
        XCTAssertEqual(try entries(home).count, 1)
    }

    func testClosedClientCancelsDaemonAndDoesNotForwardPendingLines() throws {
        let home = try home(), peer = peer()
        let received = DispatchSemaphore(value: 0), closed = DispatchSemaphore(value: 0)
        let daemon = GateDaemon(respond: { fd in
            received.signal()
            defer { closed.signal() }
            try Transport.wait(fd, events: Int16(POLLIN), deadline: ProcessInfo.processInfo.systemUptime + 2)
            var byte: UInt8 = 0
            XCTAssertEqual(recv(fd, &byte, 1, 0), 0)
        })
        let gate = CuaGate(home: home, audit: try AuditLog(directory: home + "/logs"), own: own,
                           inspect: { _ in peer }, connect: { _ in try daemon.connect() })
        try pair(gate) { fd in
            try Transport.write(Data("{\"method\":\"list\"}\n{\"method\":\"metadata\"}\n".utf8), to: fd)
            XCTAssertEqual(received.wait(timeout: .now() + 2), .success)
            shutdown(fd, SHUT_RDWR)
            XCTAssertEqual(closed.wait(timeout: .now() + 2), .success)
        }
        XCTAssertEqual(daemon.requests.count, 1)
        XCTAssertEqual(try entries(home).count, 1)
    }

    func testStopCancelsActiveDaemonAndWaitsForWorker() throws {
        let home = try home(), peer = peer()
        let received = DispatchSemaphore(value: 0), closed = DispatchSemaphore(value: 0)
        let daemon = GateDaemon(respond: { fd in
            received.signal()
            defer { closed.signal() }
            try Transport.wait(fd, events: Int16(POLLIN), deadline: ProcessInfo.processInfo.systemUptime + 2)
            var byte: UInt8 = 0
            XCTAssertEqual(recv(fd, &byte, 1, 0), 0)
        })
        let gate = CuaGate(home: home, audit: try AuditLog(directory: home + "/logs"), own: own,
                           inspect: { _ in peer }, connect: { _ in try daemon.connect() })
        try pair(gate) { fd in
            try Transport.write(Data("{\"method\":\"list\"}\n".utf8), to: fd)
            XCTAssertEqual(received.wait(timeout: .now() + 2), .success)
            gate.stop()
            XCTAssertEqual(closed.wait(timeout: .now() + 2), .success)
            XCTAssertEqual(try readAll(fd), Data())
        }
        XCTAssertEqual(try entries(home).count, 1)
    }

    func testRelayTimeoutUsesInactivityAndStreamsBeforeEOF() throws {
        let home = try home(), peer = peer()
        let daemon = GateDaemon(respond: { fd in
            for _ in 0..<5 {
                try Transport.write(Data("chunk".utf8), to: fd)
                Thread.sleep(forTimeInterval: 0.1)
            }
        })
        let gate = CuaGate(home: home, audit: try AuditLog(directory: home + "/logs"), own: own,
                           relayTimeout: 0.3, inspect: { _ in peer }, connect: { _ in try daemon.connect() })
        try pair(gate) { fd in
            try Transport.write(Data("{\"method\":\"list\"}\n".utf8), to: fd)
            // The first chunk must be delivered while the daemon is still open.
            try Transport.wait(fd, events: Int16(POLLIN), deadline: ProcessInfo.processInfo.systemUptime + 0.3)
            XCTAssertEqual(try readAll(fd), Data(String(repeating: "chunk", count: 5).utf8))
        }
        XCTAssertEqual(try entries(home).count, 1)
    }

    func testIdleDaemonTimeoutClosesBothSidesWithoutSecondAudit() throws {
        let home = try home(), peer = peer(), closed = DispatchSemaphore(value: 0)
        let daemon = GateDaemon(respond: { fd in
            defer { closed.signal() }
            try Transport.wait(fd, events: Int16(POLLIN), deadline: ProcessInfo.processInfo.systemUptime + 2)
            var byte: UInt8 = 0
            XCTAssertEqual(recv(fd, &byte, 1, 0), 0)
        })
        let gate = CuaGate(home: home, audit: try AuditLog(directory: home + "/logs"), own: own,
                           relayTimeout: 0.05, inspect: { _ in peer }, connect: { _ in try daemon.connect() })
        try pair(gate) { fd in
            try Transport.write(Data("{\"method\":\"list\"}\n".utf8), to: fd)
            XCTAssertEqual(try readAll(fd), Data())
            XCTAssertEqual(closed.wait(timeout: .now() + 2), .success)
        }
        XCTAssertEqual(try entries(home).count, 1)
    }

    func testUnsafeSocketPathsArePreserved() throws {
        let home = try home()
        try PrivateFiles.directory(home + "/run")
        let path = home + "/run/cua.sock"
        let data = Data("must survive".utf8)
        try data.write(to: URL(fileURLWithPath: path))
        let gate = CuaGate(home: home, audit: try AuditLog(directory: home + "/logs"))
        XCTAssertThrowsError(try gate.start())
        XCTAssertEqual(try Data(contentsOf: URL(fileURLWithPath: path)), data)
        try FileManager.default.moveItem(atPath: path, toPath: home + "/target")
        XCTAssertEqual(symlink(home + "/target", path), 0)
        XCTAssertThrowsError(try gate.start())
        XCTAssertEqual(try FileManager.default.destinationOfSymbolicLink(atPath: path), home + "/target")
        XCTAssertEqual(try Data(contentsOf: URL(fileURLWithPath: home + "/target")), data)
    }

    func testBusyRefusalAndLiveListenerLock() throws {
        let home = try home(), peer = peer()
        try PrivateFiles.directory(home + "/run")
        let probe = try bindListener(home + "/probe.sock")
        close(probe); unlink(home + "/probe.sock")
        let inspected = DispatchSemaphore(value: 0)
        let audit = try AuditLog(directory: home + "/logs")
        let gate = CuaGate(home: home, audit: audit, own: own, capacity: 1, inspect: { _ in
            inspected.signal(); return peer
        })
        try gate.start(); defer { gate.stop() }
        let other = CuaGate(home: home, audit: audit)
        XCTAssertThrowsError(try other.start())
        let first = try Transport.connect(path: home + "/run/cua.sock")
        defer { close(first) }
        XCTAssertEqual(inspected.wait(timeout: .now() + 2), .success)
        let second = try Transport.connect(path: home + "/run/cua.sock")
        defer { close(second) }
        let response = try JSONDecoder().decode([String: JSONValue].self, from: Transport.readLine(second))
        XCTAssertEqual(response, ["ok": .bool(false), "error": .string("busy")])
        XCTAssertEqual(try entries(home).first?["error"], .string("busy"))
        gate.stop()
        XCTAssertFalse(FileManager.default.fileExists(atPath: home + "/run/cua.sock"))
        try other.start(); other.stop()
    }

    func testNamedDaemonRoundTripAndGateSocketLifecycle() throws {
        let home = try home(), peer = peer()
        try PrivateFiles.directory(home + "/run")
        let listener = try bindListener(home + "/run/cua-daemon.sock")
        defer { close(listener) }
        let daemon = GateDaemon(reply: Data("{ \"exact\" : true }\n".utf8))
        let done = DispatchSemaphore(value: 0)
        DispatchQueue.global().async {
            defer { done.signal() }
            do {
                try Transport.wait(listener, events: Int16(POLLIN), deadline: ProcessInfo.processInfo.systemUptime + 5)
                let fd = accept(listener, nil, nil)
                XCTAssertGreaterThanOrEqual(fd, 0)
                if fd >= 0 { daemon.serve(fd) }
            } catch { XCTFail("accept: \(error)") }
        }
        let gate = CuaGate(home: home, audit: try AuditLog(directory: home + "/logs"), own: own, inspect: { _ in peer })
        XCTAssertFalse(FileManager.default.fileExists(atPath: home + "/run/cua.sock"))
        try gate.start()
        defer { gate.stop() }
        for (path, mode) in [(home + "/run", 0o700), (home + "/run/cua.sock", 0o600)] {
            let attrs = try FileManager.default.attributesOfItem(atPath: path)
            XCTAssertEqual((attrs[.posixPermissions] as? NSNumber)?.intValue, mode)
        }
        let client = try Transport.connect(path: home + "/run/cua.sock")
        defer { close(client) }
        let request = Data("{\"method\":\"list\"}\n".utf8)
        try Transport.write(request, to: client)
        XCTAssertEqual(try readAll(client), daemon.reply)
        XCTAssertEqual(done.wait(timeout: .now() + 3), .success)
        XCTAssertEqual(daemon.requests, [request])
        gate.stop()
        XCTAssertFalse(FileManager.default.fileExists(atPath: home + "/run/cua.sock"))
        try gate.start() // Cancellation must not unlink a replacement listener.
        XCTAssertTrue(FileManager.default.fileExists(atPath: home + "/run/cua.sock"))
    }
}
