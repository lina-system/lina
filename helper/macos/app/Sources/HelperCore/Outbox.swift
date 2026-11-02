import Foundation
import Darwin

/// Serialized, descriptor-relative access: never follows outbox or entry symlinks.
public final class Outbox: Sendable {
    private let path: String
    private let lock = Locked(())
    public init(home: String) { path = home + "/outbox" }

    private func directory() throws -> Int32 {
        try PrivateFiles.directory(path)
        let fd = open(path, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC)
        guard fd >= 0 else { throw HelperError(.internalError, "cannot open outbox") }
        return fd
    }
    public func cleanup(now: Date = Date()) throws {
        try lock.withLock { _ in
            let fd = try directory()
            // fdopendir takes ownership of fd.
            guard let dir = fdopendir(fd) else { close(fd); throw HelperError(.internalError, "cannot list outbox") }
            defer { closedir(dir) }
            while let entry = readdir(dir) {
                let name = withUnsafePointer(to: &entry.pointee.d_name) {
                    $0.withMemoryRebound(to: CChar.self, capacity: Int(entry.pointee.d_namlen) + 1) { String(cString: $0) }
                }
                if name == "." || name == ".." { continue }
                var info = stat()
                guard fstatat(fd, name, &info, AT_SYMLINK_NOFOLLOW) == 0 else { continue }
                let modified = Double(info.st_mtimespec.tv_sec) + Double(info.st_mtimespec.tv_nsec) / 1e9
                if info.st_mode & S_IFMT == S_IFREG, now.timeIntervalSince1970 - modified > 600 {
                    guard unlinkat(fd, name, 0) == 0 || errno == ENOENT else {
                        throw HelperError(.internalError, "cannot remove expired outbox file")
                    }
                }
            }
        }
    }
    func writePNG(_ data: Data) throws -> String {
        try lock.withLock { _ in
            let directoryFD = try directory()
            defer { close(directoryFD) }
            let name = UUID().uuidString + ".png"
            let fd = openat(directoryFD, name, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0o600)
            guard fd >= 0 else { throw HelperError(.internalError, "cannot create screenshot") }
            defer { close(fd) }
            do {
                try data.withUnsafeBytes { bytes in
                    var offset = 0
                    while offset < bytes.count {
                        let count = write(fd, bytes.baseAddress!.advanced(by: offset), bytes.count - offset)
                        if count < 0 && errno == EINTR { continue }
                        guard count > 0 else { throw HelperError(.internalError, "cannot write screenshot") }
                        offset += count
                    }
                }
            } catch { unlinkat(directoryFD, name, 0); throw error }
            return path + "/" + name
        }
    }
}
