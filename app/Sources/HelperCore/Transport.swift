import Foundation
import Darwin

public enum Transport {
    // AX trees can exceed the request ceiling; responses remain bounded.
    public static let responseLimit = 128 * 1024 * 1024
    public static let ioTimeout: TimeInterval = 3

    static func prepare(_ fd: Int32) throws {
        guard fcntl(fd, F_SETFL, fcntl(fd, F_GETFL) | O_NONBLOCK) == 0,
              fcntl(fd, F_SETFD, FD_CLOEXEC) == 0 else { throw HelperError(.internalError, "socket setup failed") }
        var one: Int32 = 1
        guard setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, socklen_t(MemoryLayout.size(ofValue: one))) == 0 else {
            throw HelperError(.internalError, "socket setup failed")
        }
    }

    static func address(_ path: String) throws -> sockaddr_un {
        var address = sockaddr_un()
        let bytes = Array(path.utf8) + [UInt8(0)]
        guard path.hasPrefix("/"), !path.utf8.contains(0), bytes.count <= MemoryLayout.size(ofValue: address.sun_path) else {
            throw HelperError(.badRequest, "socket path must be absolute and fit sockaddr_un")
        }
        address.sun_family = sa_family_t(AF_UNIX)
        address.sun_len = UInt8(MemoryLayout<sockaddr_un>.size)
        withUnsafeMutableBytes(of: &address.sun_path) { destination in destination.copyBytes(from: bytes) }
        return address
    }

    static func withAddress<T>(_ path: String, _ body: (UnsafePointer<sockaddr>, socklen_t) throws -> T) throws -> T {
        var address = try address(path)
        return try withUnsafePointer(to: &address) {
            try $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { try body($0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
        }
    }

    static func wait(_ fd: Int32, events: Int16, deadline: TimeInterval) throws {
        while true {
            let remaining = deadline - ProcessInfo.processInfo.systemUptime
            guard remaining > 0 else { throw HelperError(.timeout, "socket I/O deadline exceeded") }
            var item = pollfd(fd: fd, events: events, revents: 0)
            let result = poll(&item, 1, Int32(min(remaining * 1000 + 1, Double(Int32.max))))
            if result < 0 && errno == EINTR { continue }
            if result == 0 { throw HelperError(.timeout, "socket I/O deadline exceeded") }
            guard result > 0, item.revents & Int16(POLLNVAL) == 0 else {
                throw HelperError(.internalError, "socket poll failed")
            }
            return
        }
    }

    public static func readLine(_ fd: Int32, timeout: TimeInterval = ioTimeout, maximumBytes: Int = LineFrame.limit) throws -> Data {
        let deadline = ProcessInfo.processInfo.systemUptime + timeout
        var frame = LineFrame(maximumBytes: maximumBytes)
        var bytes = [UInt8](repeating: 0, count: 4096)
        while true {
            try wait(fd, events: Int16(POLLIN), deadline: deadline)
            let count = recv(fd, &bytes, bytes.count, 0)
            if count < 0 && (errno == EINTR || errno == EAGAIN) { continue }
            guard count > 0 else { throw HelperError(.badRequest, "connection ended before newline") }
            if let line = try frame.append(Data(bytes.prefix(count))) { return line }
        }
    }

    public static func write(_ data: Data, to fd: Int32, timeout: TimeInterval = ioTimeout) throws {
        let deadline = ProcessInfo.processInfo.systemUptime + timeout
        try data.withUnsafeBytes { bytes in
            var offset = 0
            while offset < bytes.count {
                try wait(fd, events: Int16(POLLOUT), deadline: deadline)
                let n = send(fd, bytes.baseAddress!.advanced(by: offset), bytes.count - offset, 0)
                if n < 0 && (errno == EINTR || errno == EAGAIN) { continue }
                guard n > 0 else { throw HelperError(.internalError, "socket write failed") }
                offset += n
            }
        }
    }

    public static func connect(path: String) throws -> Int32 {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw HelperError(.internalError, "cannot create socket") }
        do {
            try prepare(fd)
            let result = try withAddress(path) { Darwin.connect(fd, $0, $1) }
            if result != 0 {
                guard errno == EINPROGRESS else { throw HelperError(.internalError, "cannot connect to helper") }
                try wait(fd, events: Int16(POLLOUT), deadline: ProcessInfo.processInfo.systemUptime + ioTimeout)
                var error: Int32 = 0; var length = socklen_t(MemoryLayout<Int32>.size)
                guard getsockopt(fd, SOL_SOCKET, SO_ERROR, &error, &length) == 0, error == 0 else {
                    throw HelperError(.internalError, "cannot connect to helper")
                }
            }
            return fd
        } catch { close(fd); throw error }
    }

    public static func exchange(_ request: Request, path: String) throws -> Response {
        let fd = try connect(path: path)
        defer { close(fd) }
        let line = try JSONEncoder().encode(request) + Data([10])
        guard line.count <= LineFrame.limit else { throw HelperError(.badRequest, "request exceeds 64 KiB") }
        try write(line, to: fd)
        // Includes the server's action deadline and its 3-second response write budget.
        let data = try readLine(fd, timeout: request.action == "screenshot" ? 18 : 13, maximumBytes: responseLimit)
        guard let response = try? JSONDecoder().decode(Response.self, from: data), response.v == 1,
              response.id == request.id || (!response.ok && response.id.isEmpty),
              response.ok ? (response.result != nil && response.error == nil) : response.error != nil else {
            throw HelperError(.badRequest, "invalid helper response")
        }
        return response
    }
}
