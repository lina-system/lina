import Foundation
import Darwin

/// The public cua socket. Each line is audited before a fresh daemon connection
/// receives it. Replies are opaque byte streams, bounded only by inactivity.
public final class CuaGate: @unchecked Sendable {
    static let requestLimit = 4 * 1024 * 1024 // Includes the newline.
    private let path: String
    private let daemonPath: String
    private let audit: AuditLog
    private let lease: CuaInputLease
    private let now: @Sendable () -> TimeInterval
    private struct Identity {
        let holder: String
        let seen: TimeInterval
    }
    private let identities = Locked<[pid_t: Identity]>([:])
    private static let inputTools: Set<String> = [
        "click", "double_click", "right_click", "drag", "scroll", "move_cursor", "type_text", "type_text_chars",
        "press_key", "hotkey", "set_value", "invoke_menu", "launch_app", "bring_to_front", "kill_app",
        "run_steps", "run_script", "set_window_frame", "clipboard_write", "browser_click", "browser_type",
        "browser_navigate", "browser_pointer", "browser_select", "browser_set_input_files", "browser_dialog",
        "browser_download", "page", "replay_trajectory",
    ]
    private let own: CodeIdentity
    private let development: Bool
    private let capacity: Int
    private let readTimeout: TimeInterval
    private let relayTimeout: TimeInterval
    private let inspect: @Sendable (Int32) -> Peer
    private let connect: @Sendable (String) throws -> Int32
    private let queue = DispatchQueue(label: "app.lina-os.helper.cua-gate")
    private let workers = DispatchGroup()
    // Listener and connection membership are owned by queue.
    private var source: DispatchSourceRead?
    private var connections: [Int32: GateConnection] = [:]

    init(home: String, audit: AuditLog, lease: CuaInputLease = CuaInputLease(),
         own: CodeIdentity = PeerAuthorization.ownIdentity(),
         development: Bool = false, capacity: Int = 16, readTimeout: TimeInterval = 10,
         relayTimeout: TimeInterval = 120,
         now: @escaping @Sendable () -> TimeInterval = { ProcessInfo.processInfo.systemUptime },
         inspect: @escaping @Sendable (Int32) -> Peer = PeerAuthorization.inspect(socket:),
         connect: @escaping @Sendable (String) throws -> Int32 = Transport.connect(path:)) {
        path = home + "/run/cua.sock"; daemonPath = home + "/run/cua-daemon.sock"
        self.audit = audit; self.own = own; self.development = development
        self.lease = lease; self.now = now
        self.capacity = capacity; self.readTimeout = readTimeout; self.relayTimeout = relayTimeout
        self.inspect = inspect; self.connect = connect
    }

    func start() throws {
        try queue.sync {
            guard source == nil else { throw HelperError(.busy, "cua gate already started") }
            let directory = (path as NSString).deletingLastPathComponent
            try PrivateFiles.directory(directory)
            let lockFD = open(directory + "/cua-gate.lock", O_CREAT | O_RDWR | O_NOFOLLOW | O_NONBLOCK | O_CLOEXEC, 0o600)
            guard lockFD >= 0 else { throw HelperError(.internalError, "cannot open cua gate lock") }
            var info = stat()
            guard fstat(lockFD, &info) == 0, info.st_mode & S_IFMT == S_IFREG,
                  info.st_uid == getuid(), info.st_nlink == 1, flock(lockFD, LOCK_EX | LOCK_NB) == 0 else {
                close(lockFD); throw HelperError(.busy, "cua gate lock unavailable")
            }
            let fd = socket(AF_UNIX, SOCK_STREAM, 0)
            guard fd >= 0 else { close(lockFD); throw HelperError(.internalError, "cannot create cua gate listener") }
            var bound = false
            do {
                try Transport.prepare(fd)
                try Self.removeStaleSocket(path)
                let result = try Transport.withAddress(path) { bind(fd, $0, $1) }
                guard result == 0 else { throw HelperError(.internalError, "cannot bind cua gate (errno \(errno))") }
                bound = true
                guard chmod(path, 0o600) == 0, listen(fd, 16) == 0 else {
                    throw HelperError(.internalError, "cannot listen on cua gate")
                }
                let source = DispatchSource.makeReadSource(fileDescriptor: fd, queue: queue)
                source.setEventHandler { [weak self] in self?.acceptClients(fd) }
                let path = path
                source.setCancelHandler { close(fd); unlink(path); close(lockFD) }
                self.source = source; source.resume()
            } catch {
                close(fd); if bound { unlink(path) }; close(lockFD)
                throw error
            }
        }
    }

    /// Called only while the supervisor holds helper.lock, including startup under
    /// observe. Like HelperServer, never remove a non-socket or another user's file.
    static func removeStaleSocket(_ path: String) throws {
        var info = stat()
        if lstat(path, &info) == 0 {
            guard info.st_mode & S_IFMT == S_IFSOCK, info.st_uid == getuid() else {
                throw HelperError(.internalError, "unsafe existing cua socket path")
            }
            guard unlink(path) == 0 else { throw HelperError(.internalError, "cannot remove stale cua socket") }
        } else if errno != ENOENT { throw HelperError(.internalError, "cannot inspect cua socket path") }
    }

    func stop() {
        queue.sync {
            source?.cancel(); source = nil
            for connection in connections.values { connection.cancel() }
        }
        queue.sync {} // Release listener and lock before a replacement can start.
        workers.wait() // No old request may reach a replacement daemon.
    }
    deinit { source?.cancel() }

    private func acceptClients(_ listener: Int32) {
        for _ in 0..<32 {
            let fd = accept(listener, nil, nil)
            if fd < 0 { if errno == EINTR { continue }; return }
            do { try Transport.prepare(fd) } catch { close(fd); continue }
            guard connections.count < capacity else {
                let peer = inspect(fd)
                var message = "busy"
                do { try audit.recordCuaRequest(nil, peer: peer, error: message) }
                catch { message = "audit unavailable" }
                // A single nonblocking write; overload never creates extra workers.
                let line = Self.errorLine(message)
                line.withUnsafeBytes { _ = send(fd, $0.baseAddress, $0.count, 0) }
                close(fd); continue
            }
            let connection = GateConnection(fd)
            connections[fd] = connection; workers.enter()
            DispatchQueue.global().async { self.serve(connection) }
        }
    }

    /// Socketpair entry point for exercising the same handler without bind().
    /// Takes ownership of fd, just like an accepted connection.
    func handle(_ fd: Int32) {
        let connection = GateConnection(fd)
        queue.sync { connections[fd] = connection; workers.enter() }
        serve(connection)
    }

    private func serve(_ connection: GateConnection) {
        let client = connection.client
        defer {
            queue.sync { connections.removeValue(forKey: client); connection.finish() }
            workers.leave()
        }
        let peer = inspect(client)
        guard PeerAuthorization.allowsCua(uid: peer.uid, helperUID: getuid(), own: own,
                                          peer: peer.identity, development: development) else {
            refuse("peer identity refused", request: nil, peer: peer, client: client); return
        }
        var pending = Data()
        while !connection.cancelled {
            var request: [String: JSONValue]?
            var line: Data
            do {
                line = try readRequest(client, pending: &pending)
                guard String(data: line, encoding: .utf8) != nil, !line.contains(0),
                      let object = try? JSONDecoder().decode([String: JSONValue].self, from: line) else {
                    throw HelperError(.badRequest, "expected a JSON object")
                }
                request = object
                if let adapted = Self.adaptForDriver(object) {
                    request = adapted
                    line = try WireJSON.encode(adapted)
                }
            } catch {
                let message = (error as? HelperError)?.detail ?? "invalid request"
                refuse(message, request: request, peer: peer, client: client); return
            }
            let identity = leaseIdentity(request, peer: peer)
            if request?["method"]?.string == "call", let name = request?["name"]?.string,
               Self.inputTools.contains(name), let held = lease.refusal(identity: identity) {
                refuse(held.detail, request: request, peer: peer, client: client,
                       auditError: "lease held by \(held.holder)", exitCode: 75)
                return
            }
            do { try audit.recordCuaRequest(request, peer: peer) }
            catch { try? Transport.write(Self.errorLine("audit unavailable"), to: client); return }
            // From here on there must never be a second audit entry, including for
            // a failed daemon connection or interrupted/partially delivered reply.
            do {
                guard !connection.cancelled else { return }
                let daemon = try connect(daemonPath)
                guard connection.attach(daemon) else { return }
                do {
                    try Transport.write(line, to: daemon, timeout: readTimeout)
                    try relay(daemon, to: client, pending: &pending)
                } catch { connection.detach(); return }
                connection.detach()
            } catch {
                try? Transport.write(Self.errorLine("daemon unavailable"), to: client); return
            }
            // The upstream protocol closes after the reply. Preserve that EOF for
            // ordinary clients; pipelined bytes already received get another turn.
            if pending.isEmpty {
                var byte: UInt8 = 0
                let count = recv(client, &byte, 1, MSG_PEEK)
                if count <= 0 { return }
            }
        }
    }

    /// MCP opens a fresh gate connection for each call. Remember explicit session
    /// labels by the authenticated proxy PID, not its parent or the socket fd.
    private func leaseIdentity(_ request: [String: JSONValue]?, peer: Peer) -> String? {
        identities.withLock { identities in
            let time = now()
            identities = identities.filter { time - $0.value.seen <= 600 }
            if let session = request?["args"]?.object?["session"]?.string, !session.isEmpty {
                if let pid = peer.pid {
                    if identities[pid] == nil, identities.count >= 256,
                       let oldest = identities.min(by: { $0.value.seen < $1.value.seen })?.key {
                        identities.removeValue(forKey: oldest)
                    }
                    identities[pid] = Identity(holder: session, seen: time)
                }
                return session
            }
            return peer.pid.flatMap { identities[$0]?.holder }
        }
    }

    /// Compatibility with clients written for cua-driver 0.21 (Hermes' computer_use):
    /// - 0.38's `get_window_state` returns only `tree_markdown` unless `tree_format` is given; 0.21 always
    ///   returned the structured `elements` array with per-element `element_token`, which those clients read.
    ///   A request without `tree_format` gets `"both"`.
    /// - 0.38 identifies elements by `element_token` and rejects `element_index` as an unknown argument. When both
    ///   are present the token is authoritative, so the index is dropped.
    /// Returns nil when the request needs no change (it is then forwarded byte for byte).
    static func adaptForDriver(_ request: [String: JSONValue]) -> [String: JSONValue]? {
        guard request["method"]?.string == "call", let name = request["name"]?.string,
              var args = request["args"]?.object else { return nil }
        var changed = false
        if name == "get_window_state", args["tree_format"] == nil {
            args["tree_format"] = .string("both"); changed = true
        }
        if args["element_token"]?.string != nil, args["element_index"] != nil {
            args.removeValue(forKey: "element_index"); changed = true
        }
        guard changed else { return nil }
        var adapted = request
        adapted["args"] = .object(args)
        return adapted
    }

    private func readRequest(_ client: Int32, pending: inout Data) throws -> Data {
        let deadline = ProcessInfo.processInfo.systemUptime + readTimeout
        var buffer = [UInt8](repeating: 0, count: 16 * 1024)
        var scanned = 0
        while true {
            if let end = pending.dropFirst(scanned).firstIndex(of: 10) {
                let length = pending.distance(from: pending.startIndex, to: end) + 1
                guard length <= Self.requestLimit else { throw HelperError(.badRequest, "request exceeds 4 MiB") }
                let line = Data(pending.prefix(length)); pending = Data(pending.dropFirst(length))
                return line
            }
            guard pending.count < Self.requestLimit else { throw HelperError(.badRequest, "request exceeds 4 MiB") }
            scanned = pending.count
            do { try Transport.wait(client, events: Int16(POLLIN), deadline: deadline) }
            catch { throw HelperError(.timeout, "request timeout") }
            let count = recv(client, &buffer, min(buffer.count, Self.requestLimit - pending.count), 0)
            if count < 0 && (errno == EINTR || errno == EAGAIN) { continue }
            guard count > 0 else { throw HelperError(.badRequest, "connection ended before newline") }
            pending.append(contentsOf: buffer.prefix(count))
        }
    }

    /// Only one 64 KiB reply chunk is retained. The next request buffer is bounded
    /// at 4 MiB; excess pipelining receives ordinary kernel socket backpressure.
    /// Reset the deadline on reply progress, never on unrelated client traffic.
    private func relay(_ daemon: Int32, to client: Int32, pending: inout Data) throws {
        var deadline = ProcessInfo.processInfo.systemUptime + relayTimeout
        var buffer = [UInt8](repeating: 0, count: 64 * 1024)
        var output = Data(), offset = 0
        while true {
            let remaining = deadline - ProcessInfo.processInfo.systemUptime
            guard remaining > 0 else { throw HelperError(.timeout, "daemon inactivity timeout") }
            var items = [pollfd(fd: output.isEmpty ? daemon : -1, events: Int16(POLLIN), revents: 0),
                         pollfd(fd: client, events: (pending.count < Self.requestLimit ? Int16(POLLIN) : 0) |
                            (output.isEmpty ? 0 : Int16(POLLOUT)), revents: 0)]
            let n = poll(&items, 2, Int32(min(remaining * 1000 + 1, Double(Int32.max))))
            if n < 0 && errno == EINTR { continue }
            guard n > 0 else { throw HelperError(.timeout, "daemon inactivity timeout") }
            guard items.allSatisfy({ $0.revents & Int16(POLLNVAL | POLLERR) == 0 }) else {
                throw HelperError(.internalError, "relay socket failed")
            }
            if items[1].revents & Int16(POLLHUP) != 0 { throw HelperError(.badRequest, "client closed") }
            if items[1].revents & Int16(POLLIN) != 0, pending.count < Self.requestLimit {
                let count = recv(client, &buffer, min(buffer.count, Self.requestLimit - pending.count), 0)
                if count == 0 { throw HelperError(.badRequest, "client closed") }
                if count < 0 && errno != EINTR && errno != EAGAIN { throw HelperError(.internalError, "client read failed") }
                if count > 0 { pending.append(contentsOf: buffer.prefix(count)) }
            }
            if !output.isEmpty, items[1].revents & Int16(POLLOUT) != 0 {
                let count = output.withUnsafeBytes { send(client, $0.baseAddress!.advanced(by: offset), $0.count - offset, 0) }
                if count < 0 && (errno == EINTR || errno == EAGAIN) { continue }
                guard count > 0 else { throw HelperError(.internalError, "client write failed") }
                offset += count; deadline = ProcessInfo.processInfo.systemUptime + relayTimeout
                if offset == output.count { output.removeAll(keepingCapacity: true); offset = 0 }
            }
            if output.isEmpty, items[0].revents & Int16(POLLIN | POLLHUP) != 0 {
                let count = recv(daemon, &buffer, buffer.count, 0)
                if count < 0 && (errno == EINTR || errno == EAGAIN) { continue }
                guard count >= 0 else { throw HelperError(.internalError, "daemon read failed") }
                if count == 0 { return }
                output.append(contentsOf: buffer.prefix(count)); deadline = ProcessInfo.processInfo.systemUptime + relayTimeout
            }
        }
    }

    private func refuse(_ error: String, request: [String: JSONValue]?, peer: Peer, client: Int32,
                        auditError: String? = nil, exitCode: Int? = nil) {
        var message = error
        do { try audit.recordCuaRequest(request, peer: peer, error: auditError ?? error) }
        catch { message = "audit unavailable" }
        try? Transport.write(Self.errorLine(message, exitCode: exitCode), to: client)
    }
    private static func errorLine(_ error: String, exitCode: Int? = nil) -> Data {
        var response: [String: JSONValue] = ["ok": .bool(false), "error": .string(error)]
        if let exitCode { response["exit_code"] = .number(Double(exitCode)) }
        return (try? WireJSON.encode(response)) ?? Data()
    }
}

/// Cancellation shuts sockets down but only the worker closes them. The lock
/// prevents stop() from touching a descriptor after close/reuse by another thread.
private final class GateConnection: @unchecked Sendable {
    let client: Int32
    private let lock = NSLock()
    private var daemon: Int32?
    private var stopped = false
    var cancelled: Bool { lock.lock(); defer { lock.unlock() }; return stopped }
    init(_ client: Int32) { self.client = client }
    func attach(_ fd: Int32) -> Bool {
        lock.lock(); defer { lock.unlock() }
        guard !stopped else { close(fd); return false }
        daemon = fd; return true
    }
    func detach() {
        lock.lock(); defer { lock.unlock() }
        if let daemon { close(daemon) }; daemon = nil
    }
    func cancel() {
        lock.lock(); defer { lock.unlock() }
        stopped = true; shutdown(client, SHUT_RDWR)
        if let daemon { shutdown(daemon, SHUT_RDWR) }
    }
    func finish() {
        lock.lock(); defer { lock.unlock() }
        stopped = true
        if let daemon { close(daemon) }; daemon = nil
        close(client)
    }
}
