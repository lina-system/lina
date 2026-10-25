import Foundation
import Darwin

/// A built-in profile or v0 explicit actions, from a trusted root policy.
/// Limits stay attached to the action and are passed to its handler.
public struct Policy: Sendable {
    private let actions: [String: JSONValue]
    public let profile: String
    public var runsCuaDriver: Bool { profile == "operate" }
    public let source: String
    public var metadata: [String: JSONValue] {
        ["profile": .string(profile), "source": .string(source)]
    }
    public static let restricted = Policy(actions: [:], profile: "restricted", source: "restricted")
    public static let observe = builtIn("observe", source: "builtin")

    // Compiled in, never read from the bundle: in user mode the bundle is user-writable and
    // resource files are not checked at run time. app/Resources/PolicyProfiles.json mirrors this
    // for the Python installer; PolicyProfileTests checks the two agree.
    static let profiles: [String: [String: JSONValue]]? = [
        "observe": [
            "cua.status": .object([:]),
            "screenshot": .object(["timeout_s": .number(15)]),
            "ax.tree": .object(["max_depth": .number(8), "max_nodes": .number(2000)]),
        ],
        "operate_additions": [
            "cua.restart": .object([:]),
            "ax.press": .object([:]), "input.click": .object([:]), "input.keys": .object([:]), "app.open": .object([:]),
        ],
    ]

    private static func builtIn(_ profile: String, source: String) -> Self {
        guard let profiles, var actions = profiles["observe"],
              let additions = profiles["operate_additions"],
              actions.values.allSatisfy({ $0.object != nil }),
              additions.values.allSatisfy({ $0.object != nil }) else { return .restricted }
        if profile == "operate" { actions.merge(additions) { _, new in new } }
        return Policy(actions: actions, profile: profile, source: source)
    }
    public func allows(_ action: String) -> Bool {
        action == "status" || action == "permissions" || actions[action]?.object != nil
    }
    public func limits(for action: String) -> [String: JSONValue] { actions[action]?.object ?? [:] }
    public static func parse(_ data: Data, owner: uid_t, mode: mode_t) -> Self {
        guard data.count <= 1_048_576, owner == 0, mode & 0o022 == 0,
              let root = try? JSONDecoder().decode([String: JSONValue].self, from: data) else { return .restricted }
        if let selected = root["profile"] {
            guard root["actions"] == nil, let profile = selected.string,
                  profile == "observe" || profile == "operate" else { return .restricted }
            return builtIn(profile, source: "root")
        }
        guard let actions = root["actions"]?.object,
              actions.values.allSatisfy({ $0.object != nil }) else { return .restricted }
        return Policy(actions: actions, profile: "custom", source: "root")
    }
    public static func load(path: String) -> Self {
        // Check the opened inode, never a stat followed by a separate path-based read.
        let fd = open(path, O_RDONLY | O_NOFOLLOW | O_NONBLOCK | O_CLOEXEC)
        guard fd >= 0 else { return errno == ENOENT ? .observe : .restricted }
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
