import Foundation
import Darwin
import OSLog

public final class HelperServer: @unchecked Sendable {
    private let configuration: Configuration
    private let dispatcher: ActionDispatcher
    private let audit: AuditLog
    private let outbox: Outbox?
    private let cua: CuaDriverSupervisor?
    private let own: CodeIdentity
    private let inspect: @Sendable (Int32) -> Peer
    private let queue = DispatchQueue(label: "app.lina-os.helper.listener")
    private let clients: DispatchSemaphore
    // Accessed only on queue, including start/stop.
    private var source: DispatchSourceRead?
    private var cleanupTimer: DispatchSourceTimer?
    private let logger = Logger(subsystem: "app.lina-os.helper", category: "server")

    public init(configuration: Configuration, dispatcher: ActionDispatcher, audit: AuditLog,
                outbox: Outbox? = nil, cua: CuaDriverSupervisor? = nil,
                own: CodeIdentity = PeerAuthorization.ownIdentity(), capacity: Int = 16,
                inspect: @escaping @Sendable (Int32) -> Peer = PeerAuthorization.inspect(socket:)) {
        self.configuration = configuration; self.dispatcher = dispatcher; self.audit = audit
        self.outbox = outbox
        self.cua = cua
        self.own = own; self.inspect = inspect; clients = DispatchSemaphore(value: capacity)
    }

    public func start() throws {
        try queue.sync {
            guard source == nil else { throw HelperError(.busy, "server already started") }
            let directory = (configuration.socketPath as NSString).deletingLastPathComponent
            try PrivateFiles.directory(configuration.home)
            try outbox?.cleanup()
            try PrivateFiles.directory(directory)
            let lockPath = directory + "/helper.lock"
            let lockFD = open(lockPath, O_CREAT | O_RDWR | O_NOFOLLOW | O_NONBLOCK | O_CLOEXEC, 0o600)
            guard lockFD >= 0 else { throw HelperError(.internalError, "cannot open server lock") }
            var lockInfo = stat()
            guard fstat(lockFD, &lockInfo) == 0, lockInfo.st_mode & S_IFMT == S_IFREG,
                  lockInfo.st_uid == getuid(), lockInfo.st_nlink == 1, flock(lockFD, LOCK_EX | LOCK_NB) == 0 else {
                close(lockFD); throw HelperError(.busy, "server lock unavailable")
            }
            let fd = socket(AF_UNIX, SOCK_STREAM, 0)
            guard fd >= 0 else { close(lockFD); throw HelperError(.internalError, "cannot create listener") }
            var bound = false
            do {
                try Transport.prepare(fd)
                var existing = stat()
                if lstat(configuration.socketPath, &existing) == 0 {
                    guard existing.st_mode & S_IFMT == S_IFSOCK, existing.st_uid == getuid() else {
                        throw HelperError(.internalError, "unsafe existing socket path")
                    }
                    guard unlink(configuration.socketPath) == 0 else { throw HelperError(.internalError, "cannot remove stale socket") }
                } else if errno != ENOENT { throw HelperError(.internalError, "cannot inspect socket path") }
                let result = try Transport.withAddress(configuration.socketPath) { bind(fd, $0, $1) }
                guard result == 0 else { throw HelperError(.internalError, "cannot bind socket (errno \(errno))") }
                bound = true
                guard chmod(configuration.socketPath, 0o600) == 0, listen(fd, 16) == 0 else {
                    throw HelperError(.internalError, "cannot listen on socket")
                }
                let source = DispatchSource.makeReadSource(fileDescriptor: fd, queue: queue)
                source.setEventHandler { [weak self] in self?.acceptClients(fd) }
                // Capture resources, not self: cancellation also cleans up on deinit.
                let path = configuration.socketPath
                source.setCancelHandler { close(fd); unlink(path); close(lockFD) }
                self.source = source
                source.resume()
                let timer = DispatchSource.makeTimerSource(queue: queue)
                timer.schedule(deadline: .now() + 3600, repeating: 3600)
                timer.setEventHandler { [weak self] in
                    guard let self else { return }
                    do { try audit.cleanup() }
                    catch { logger.error("audit cleanup failed") }
                    do { try outbox?.cleanup() }
                    catch { logger.error("outbox cleanup failed") }
                }
                cleanupTimer = timer; timer.resume()
            } catch {
                close(fd); if bound { unlink(configuration.socketPath) }; close(lockFD)
                throw error
            }
        }
    }

    public func stop() {
        // Keep helper.lock until the grant-bearing child is gone. A replacement
        // helper must not start another daemon while this one is still stopping.
        cua?.shutdown()
        queue.sync { cleanupTimer?.cancel(); cleanupTimer = nil; source?.cancel(); source = nil }
        queue.sync {} // Drain the cancellation handler before another server starts.
    }
    deinit { cleanupTimer?.cancel(); source?.cancel() }

    private func acceptClients(_ listener: Int32) {
        // Bound each batch so stop and cleanup cannot be starved by a connection flood.
        for _ in 0..<32 {
            let fd = accept(listener, nil, nil)
            if fd < 0 {
                if errno == EINTR { continue }
                return
            }
            do { try Transport.prepare(fd) } catch { close(fd); continue }
            let admitted = clients.wait(timeout: .now()) == .success
            if admitted {
                DispatchQueue.global().async { [self] in
                    defer { close(fd); clients.signal() }
                    handle(fd)
                }
            } else {
                // A tiny nonblocking refusal avoids spawning unlimited overload workers.
                let response = Response.failure(HelperError(.busy, "connection capacity reached"))
                let peer = inspect(fd)
                do {
                    try audit.record(request: nil, peer: peer, response: response, elapsed: 0)
                    if let line = try? response.line() { line.withUnsafeBytes { _ = send(fd, $0.baseAddress, $0.count, 0) } }
                } catch { logger.error("audit write failed for busy refusal") }
                close(fd)
            }
        }
    }

    func handle(_ fd: Int32) {
        let started = ProcessInfo.processInfo.systemUptime
        let peer = inspect(fd)
        var request: Request?
        let authorized = PeerAuthorization.allows(uid: peer.uid, helperUID: getuid(), own: own,
                                                  peer: peer.identity, development: configuration.development)
        var response: Response
        do {
            // Do not parse or wait for input from an unauthenticated process.
            guard authorized else { throw HelperError(.notAllowed, "peer identity refused") }
            let decoded = try Request.decode(Transport.readLine(fd))
            request = decoded
            try outbox?.cleanup()
            let policy = cua?.checkPolicy() ?? Policy.load(path: configuration.policyPath)
            response = dispatcher.dispatch(decoded, policy: policy)
        } catch let error as HelperError { response = .failure(id: request?.id ?? "", error) }
        catch { response = .failure(id: request?.id ?? "", HelperError(.internalError, "request failed")) }
        do {
            try audit.record(request: request, peer: peer, response: response,
                             elapsed: ProcessInfo.processInfo.systemUptime - started)
        } catch {
            logger.error("audit write failed")
            response = .failure(id: request?.id ?? "", HelperError(.internalError, "audit unavailable"))
        }
        do { try Transport.write(response.line(), to: fd) }
        catch { logger.error("response delivery failed") }
    }
}
