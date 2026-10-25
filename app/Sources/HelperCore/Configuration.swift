import Foundation
import Darwin

public struct Configuration: Sendable {
    public let home: String
    public let socketPath: String
    public let policyPath: String
    public let auditDirectory: String
    public let development: Bool
    public init(environment: [String: String] = ProcessInfo.processInfo.environment) {
        let userHome = FileManager.default.homeDirectoryForCurrentUser.path
        development = environment["LINA_HELPER_DEV"] == "1"
        home = environment["LINA_HELPER_HOME"] ?? userHome + "/Library/Application Support/LINA Helper"
        socketPath = home + "/run/helper.sock"
        policyPath = development ? environment["LINA_HELPER_POLICY"] ?? Self.defaultPolicy : Self.defaultPolicy
        // Keep all state isolated when using a test home, including the audit log.
        auditDirectory = environment["LINA_HELPER_HOME"] != nil ? home + "/logs" : userHome + "/Library/Logs/LINA Helper"
    }
    public static let defaultPolicy = "/Library/Application Support/LINA Helper/policy.json"
}

enum PrivateFiles {
    static func directory(_ path: String) throws {
        try FileManager.default.createDirectory(atPath: path, withIntermediateDirectories: true,
                                               attributes: [.posixPermissions: 0o700])
        let fd = open(path, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC)
        guard fd >= 0 else { throw HelperError(.internalError, "cannot open private directory") }
        defer { close(fd) }
        var info = stat()
        guard fstat(fd, &info) == 0, info.st_uid == getuid(), fchmod(fd, 0o700) == 0 else {
            throw HelperError(.internalError, "unsafe private directory")
        }
    }
}
