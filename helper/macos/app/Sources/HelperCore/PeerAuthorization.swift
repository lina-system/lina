import Foundation
import Security
import Darwin

public struct CodeIdentity: Sendable {
    public let valid: Bool
    public let team: String?
    public let identifier: String?
    public init(valid: Bool, team: String?, identifier: String? = nil) {
        self.valid = valid; self.team = team; self.identifier = identifier
    }
}

public enum PeerAuthorization {
    public static func allows(uid: uid_t, helperUID: uid_t, own: CodeIdentity,
                              peer: CodeIdentity, development: Bool) -> Bool {
        guard uid == helperUID || uid == 0, own.valid, peer.valid else { return false }
        guard let team = own.team, !team.isEmpty else { return development }
        return peer.team == team
    }

    /// Gate peers must be this user (no root exception). The upstream proxy has
    /// its own team; all other peers follow the helper's normal dev/team rule.
    public static func allowsCua(uid: uid_t, helperUID: uid_t, own: CodeIdentity,
                                 peer: CodeIdentity, development: Bool) -> Bool {
        guard uid == helperUID, own.valid, peer.valid else { return false }
        if peer.team == CuaDriverPin.team, peer.identifier == CuaDriverPin.identifier { return true }
        return allows(uid: uid, helperUID: helperUID, own: own, peer: peer, development: development)
    }

    public static func ownIdentity() -> CodeIdentity {
        var code: SecCode?
        guard SecCodeCopySelf([], &code) == errSecSuccess, let code else {
            return CodeIdentity(valid: false, team: nil)
        }
        return identity(code)
    }

    private static func identity(_ code: SecCode) -> CodeIdentity {
        guard SecCodeCheckValidity(code, SecCSFlags(rawValue: kSecCSStrictValidate), nil) == errSecSuccess else {
            return CodeIdentity(valid: false, team: nil)
        }
        guard let values = signingInformation(code) else { return CodeIdentity(valid: false, team: nil) }
        return CodeIdentity(valid: true, team: values[kSecCodeInfoTeamIdentifier as String] as? String,
                            identifier: values[kSecCodeInfoIdentifier as String] as? String)
    }

    private static func signingInformation(_ code: SecCode) -> [String: Any]? {
        var staticCode: SecStaticCode?
        var info: CFDictionary?
        guard SecCodeCopyStaticCode(code, [], &staticCode) == errSecSuccess, let staticCode,
              SecCodeCopySigningInformation(staticCode, SecCSFlags(rawValue: kSecCSSigningInformation), &info) == errSecSuccess else { return nil }
        return info as? [String: Any]
    }

    public static func inspect(socket: Int32) -> Peer {
        var uid: uid_t = UInt32.max
        var gid: gid_t = UInt32.max
        guard getpeereid(socket, &uid, &gid) == 0 else { return Peer(uid: uid) }
        var peer = Peer(uid: uid)
        var token = audit_token_t()
        var length = socklen_t(MemoryLayout<audit_token_t>.size)
        guard getsockopt(socket, SOL_LOCAL, LOCAL_PEERTOKEN, &token, &length) == 0,
              length == MemoryLayout<audit_token_t>.size else { return peer }
        // Attribution is best effort; authorization still uses the kernel token.
        peer.pid = Int32(bitPattern: token.val.5)
        if let pid = peer.pid {
            var info = proc_bsdinfo()
            let size = Int32(MemoryLayout<proc_bsdinfo>.size)
            if proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, size) == size {
                peer.parentPID = Int32(exactly: info.pbi_ppid)
                if let parent = peer.parentPID, parent > 0 {
                    var path = [CChar](repeating: 0, count: 4 * Int(MAXPATHLEN))
                    let count = proc_pidpath(parent, &path, UInt32(path.count))
                    if count > 0 { peer.parentExecutable = String(decoding: path.prefix(Int(count)).prefix { $0 != 0 }.map { UInt8(bitPattern: $0) }, as: UTF8.self) }
                }
            }
        }
        let data = withUnsafeBytes(of: token) { Data($0) }
        var code: SecCode?
        let attributes = [kSecGuestAttributeAudit as String: data] as CFDictionary
        guard SecCodeCopyGuestWithAttributes(nil, attributes, [], &code) == errSecSuccess, let code else { return peer }
        peer.identity = identity(code)
        if let values = signingInformation(code), let url = values[kSecCodeInfoMainExecutable as String] as? URL {
            peer.executable = url.path
        }
        return peer
    }
}

public struct Peer: Sendable {
    public var uid: uid_t
    public var pid: Int32?
    public var executable: String?
    public var parentPID: Int32?
    public var parentExecutable: String?
    public var identity = CodeIdentity(valid: false, team: nil)
    public init(uid: uid_t) { self.uid = uid }
}
