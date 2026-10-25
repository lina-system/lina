import Foundation
import Darwin

/// Schema: {"actions":{"action.name": { ... action-specific limits ... }}}.
/// Limits stay attached to the action and are passed to its handler.
public struct Policy: Sendable {
    private let actions: [String: JSONValue]
    public static let restricted = Policy(actions: [:])
    public func allows(_ action: String) -> Bool {
        action == "status" || action == "permissions" || actions[action]?.object != nil
    }
    public func limits(for action: String) -> [String: JSONValue] { actions[action]?.object ?? [:] }
    public static func parse(_ data: Data, owner: uid_t, mode: mode_t) -> Self {
        guard owner == 0, mode & 0o022 == 0,
              let root = try? JSONDecoder().decode([String: JSONValue].self, from: data),
              let actions = root["actions"]?.object,
              actions.values.allSatisfy({ $0.object != nil }) else { return .restricted }
        return Policy(actions: actions)
    }
    public static func load(path: String) -> Self {
        // Check the opened inode, never a stat followed by a separate path-based read.
        let fd = open(path, O_RDONLY | O_NOFOLLOW | O_NONBLOCK | O_CLOEXEC)
        guard fd >= 0 else { return .restricted }
        defer { close(fd) }
        var info = stat()
        guard fstat(fd, &info) == 0, info.st_mode & S_IFMT == S_IFREG,
              info.st_uid == 0, info.st_mode & 0o022 == 0,
              info.st_size >= 0, info.st_size <= 1_048_576 else { return .restricted }
        var bytes = [UInt8](repeating: 0, count: Int(info.st_size))
        var offset = 0
        while offset < bytes.count {
            let n = bytes.withUnsafeMutableBytes { read(fd, $0.baseAddress!.advanced(by: offset), $0.count - offset) }
            if n < 0 && errno == EINTR { continue }
            guard n > 0 else { return .restricted }
            offset += n
        }
        return parse(Data(bytes), owner: info.st_uid, mode: info.st_mode)
    }
}
