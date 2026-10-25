import Foundation
import Darwin
import Security

struct CuaLaunch: Sendable {
    let executable: String
    let arguments: [String]
    let environment: [String: String]
}

struct CuaSignatureError: Error, Sendable {
    let detail: String
}

/// All child executions, including read-only CLI queries, are checked while suspended.
/// This avoids a second unchecked exec of the user-writable binary during readiness.
enum CuaProcess {
    static func spawn(_ launch: CuaLaunch, stdout: Int32? = nil) throws -> pid_t {
        var attributes: posix_spawnattr_t?
        var files: posix_spawn_file_actions_t?
        try check(posix_spawnattr_init(&attributes), "spawn attributes")
        defer { posix_spawnattr_destroy(&attributes) }
        try check(posix_spawn_file_actions_init(&files), "spawn file actions")
        defer { posix_spawn_file_actions_destroy(&files) }
        var mask = sigset_t(); sigemptyset(&mask)
        var defaults = sigset_t(); sigemptyset(&defaults)
        for value in [SIGTERM, SIGINT, SIGPIPE] { sigaddset(&defaults, value) }
        try check(posix_spawnattr_setsigmask(&attributes, &mask), "spawn signal mask")
        try check(posix_spawnattr_setsigdefault(&attributes, &defaults), "spawn signal defaults")
        try check(posix_spawnattr_setflags(&attributes, Int16(POSIX_SPAWN_START_SUSPENDED |
            POSIX_SPAWN_CLOEXEC_DEFAULT | POSIX_SPAWN_SETSIGMASK | POSIX_SPAWN_SETSIGDEF)), "spawn flags")
        try check(posix_spawn_file_actions_addopen(&files, STDIN_FILENO, "/dev/null", O_RDONLY, 0), "spawn stdin")
        try check(posix_spawn_file_actions_addopen(&files, STDERR_FILENO, "/dev/null", O_WRONLY, 0), "spawn stderr")
        if let stdout {
            try check(posix_spawn_file_actions_adddup2(&files, stdout, STDOUT_FILENO), "spawn stdout pipe")
        } else {
            try check(posix_spawn_file_actions_addopen(&files, STDOUT_FILENO, "/dev/null", O_WRONLY, 0), "spawn stdout")
        }
        let argv = ([launch.executable] + launch.arguments).map { strdup($0) } + [nil]
        let envp = launch.environment.sorted { $0.key < $1.key }.map { strdup($0.key + "=" + $0.value) } + [nil]
        defer { for p in argv + envp { free(p) } }
        var pid: pid_t = 0
        let result = argv.withUnsafeBufferPointer { args in
            envp.withUnsafeBufferPointer { env in
                posix_spawn(&pid, launch.executable, &files, &attributes, args.baseAddress!, env.baseAddress!)
            }
        }
        try check(result, "spawn cua-driver")
        return pid
    }

    static func verify(_ pid: pid_t) throws {
        do { try verifyGuest(pid) }
        catch { throw CuaSignatureError(detail: (error as? HelperError)?.detail ?? "signature check failed") }
    }

    private static func verifyGuest(_ pid: pid_t) throws {
        var code: SecCode?
        let attributes = [kSecGuestAttributePid as String: NSNumber(value: pid)] as CFDictionary
        try check(SecCodeCopyGuestWithAttributes(nil, attributes, [], &code), "guest signature lookup")
        guard let code else { throw HelperError(.internalError, "missing guest code") }
        var requirement: SecRequirement?
        try check(SecRequirementCreateWithString(CuaDriverPin.requirement as CFString, [], &requirement), "signature requirement")
        try check(SecCodeCheckValidity(code, SecCSFlags(rawValue: kSecCSStrictValidate), requirement), "signature refused")
        var staticCode: SecStaticCode?
        try check(SecCodeCopyStaticCode(code, [], &staticCode), "static guest code")
        guard let staticCode else { throw HelperError(.internalError, "missing static guest code") }
        var info: CFDictionary?
        try check(SecCodeCopySigningInformation(staticCode, SecCSFlags(rawValue: kSecCSSigningInformation), &info), "guest cdhash")
        let values = info as? [String: Any]
        guard let hash = values?[kSecCodeInfoUnique as String] as? Data,
              hash.map({ String(format: "%02x", $0) }).joined() == CuaDriverPin.cdhash else {
            throw HelperError(.internalError, "signature cdhash mismatch")
        }
    }

    static func reap(_ pid: pid_t, wait: Bool) -> Bool {
        var status: Int32 = 0
        while true {
            let result = waitpid(pid, &status, wait ? 0 : WNOHANG)
            if result == -1 && errno == EINTR { continue }
            return result == pid || (result == -1 && errno == ECHILD)
        }
    }

    static func socketReady(_ path: String) -> Bool {
        var info = stat()
        return lstat(path, &info) == 0 && info.st_mode & S_IFMT == S_IFSOCK && info.st_uid == getuid()
    }

    static func query(_ launch: CuaLaunch) throws -> String {
        var descriptors = [Int32](repeating: -1, count: 2)
        guard pipe(&descriptors) == 0 else { throw HelperError(.internalError, "cua query pipe failed") }
        defer { close(descriptors[0]); if descriptors[1] >= 0 { close(descriptors[1]) } }
        for fd in descriptors { _ = fcntl(fd, F_SETFD, FD_CLOEXEC) }
        _ = fcntl(descriptors[0], F_SETFL, O_NONBLOCK)
        let pid = try spawn(launch, stdout: descriptors[1])
        close(descriptors[1]); descriptors[1] = -1
        var reaped = false
        defer { if !reaped { kill(pid, SIGKILL); _ = reap(pid, wait: true) } }
        try verify(pid)
        guard kill(pid, SIGCONT) == 0 else { throw HelperError(.internalError, "cua query resume failed") }
        let deadline = ProcessInfo.processInfo.systemUptime + 2
        var output = Data()
        var buffer = [UInt8](repeating: 0, count: 4096)
        while ProcessInfo.processInfo.systemUptime < deadline {
            let count = read(descriptors[0], &buffer, buffer.count)
            if count > 0 {
                output.append(contentsOf: buffer.prefix(count))
                guard output.count <= 65_536 else { throw HelperError(.internalError, "cua query output too large") }
                continue
            }
            if count < 0 && errno != EAGAIN && errno != EINTR {
                throw HelperError(.internalError, "cua query read failed")
            }
            var status: Int32 = 0
            let result = waitpid(pid, &status, WNOHANG)
            if result == pid {
                reaped = true
                guard status == 0 else { throw HelperError(.internalError, "cua query exited unsuccessfully") }
                // Drain bytes written between the read and waitpid.
                while true {
                    let n = read(descriptors[0], &buffer, buffer.count)
                    if n < 0 && errno == EINTR { continue }
                    if n <= 0 { break }
                    output.append(contentsOf: buffer.prefix(n))
                    guard output.count <= 65_536 else { throw HelperError(.internalError, "cua query output too large") }
                }
                return String(decoding: output, as: UTF8.self)
            }
            if result < 0 && errno != EINTR { throw HelperError(.internalError, "cua query wait failed") }
            Thread.sleep(forTimeInterval: 0.01)
        }
        throw HelperError(.timeout, "cua query timed out")
    }

    private static func check(_ result: Int32, _ operation: String) throws {
        guard result == 0 else { throw HelperError(.internalError, "\(operation) (\(result))") }
    }
}
