import Foundation
import Security
import Darwin

public struct CodeIdentity: Sendable {
    public let valid: Bool
    public let team: String?
    public init(valid: Bool, team: String?) { self.valid = valid; self.team = team }
}

public enum PeerAuthorization {
    public static func allows(uid: uid_t, helperUID: uid_t, own: CodeIdentity,
                              peer: CodeIdentity, development: Bool) -> Bool {
        guard uid == helperUID || uid == 0, own.valid, peer.valid else { return false }
        guard let team = own.team, !team.isEmpty else { return development }
        return peer.team == team
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
        return CodeIdentity(valid: true, team: values[kSecCodeInfoTeamIdentifier as String] as? String)
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
        let data = withUnsafeBytes(of: token) { Data($0) }
        var code: SecCode?
        let attributes = [kSecGuestAttributeAudit as String: data] as CFDictionary
        guard SecCodeCopyGuestWithAttributes(nil, attributes, [], &code) == errSecSuccess, let code else { return peer }
        peer.identity = identity(code)
        // The PID is taken from the same kernel audit token used for signature validation.
        peer.pid = Int32(bitPattern: token.val.5)
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
    public var identity = CodeIdentity(valid: false, team: nil)
    public init(uid: uid_t) { self.uid = uid }
}
