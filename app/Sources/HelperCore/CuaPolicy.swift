import Foundation
import CryptoKit
import Darwin

// These are compiled into signed code, never loaded from user-writable bundle resources.
// CuaDriverTests verifies agreement with app/cua-driver.pin.
public enum CuaDriverPin {
    public static let version = "0.38.0"
    static let identifier = "cua-driver"
    static let team = "YCK386LBJ7"
    static let cdhash = "b067c86ef1ca3e63a8c86eba9fe29d61bd00ad68"
    static let requirement = "identifier \"\(identifier)\" and anchor apple generic and certificate leaf[subject.OU] = \"\(team)\""
}

enum CuaPolicy {
    static func yaml(profile: String) -> Data {
        guard profile == "operate" else { return Data("allow: []\n".utf8) }
        // v0.38.0 YAML denies tools without a matching allow entry. No wildcard/rules.
        let tools = ["get_window_state", "get_desktop_state", "get_accessibility_tree", "list_apps", "list_windows",
                     "get_screen_size", "get_cursor_position", "zoom", "check_permissions", "health_report", "get_guide",
                     "start_session", "end_session", "get_session", "list_sessions", "click", "double_click",
                     "right_click", "drag", "scroll", "move_cursor", "type_text", "press_key", "hotkey", "set_value",
                     "invoke_menu", "launch_app", "bring_to_front", "run_steps", "verify_state"]
        return Data(("allow:\n  tools:\n" + tools.map { "    - \($0)\n" }.joined()).utf8)
    }

    static func digest(_ bytes: Data) -> String {
        // Upstream policy.rs::policy_sha256 (tag cua-driver-rs-v0.38.0): SHA256 of
        // domain + big-endian u64 filename length + filename + u64 data length + data.
        let name = Data("cua-policy.yaml".utf8)
        var input = Data("cua-driver-policy-v1\0".utf8)
        for item in [name, bytes] {
            var length = UInt64(item.count).bigEndian
            withUnsafeBytes(of: &length) { input.append(contentsOf: $0) }
            input.append(item)
        }
        return SHA256.hash(data: input).map { String(format: "%02x", $0) }.joined()
    }

    static func statusDigest(_ text: String) -> String? {
        let prefix = "user policy sha256:"
        let values = text.split(separator: "\n").map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { $0.hasPrefix(prefix) }.map { $0.dropFirst(prefix.count).trimmingCharacters(in: .whitespaces) }
        guard values.count == 1, let value = values.first, value.count == 64,
              value.allSatisfy({ "0123456789abcdef".contains($0) }) else { return nil }
        return value
    }

    static func write(_ data: Data, path: String) throws {
        // Same-UID policy replacement remains possible (fleet §4); never follow a link
        // or truncate another inode while preparing this hygiene policy.
        unlink(path)
        let fd = open(path, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0o600)
        guard fd >= 0 else { throw HelperError(.internalError, "cannot create cua policy") }
        defer { close(fd) }
        guard fchmod(fd, 0o600) == 0 else { throw HelperError(.internalError, "cannot secure cua policy") }
        try data.withUnsafeBytes { bytes in
            var offset = 0
            while offset < bytes.count {
                let n = Darwin.write(fd, bytes.baseAddress!.advanced(by: offset), bytes.count - offset)
                if n < 0 && errno == EINTR { continue }
                guard n > 0 else { throw HelperError(.internalError, "cannot write cua policy") }
                offset += n
            }
        }
    }
}
