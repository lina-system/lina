import XCTest
import Darwin
@testable import HelperCore

/// Opt-in native acceptance tests against the built upstream binary. These never
/// run a tool that captures the screen or posts input, and sign copies only ad-hoc.
final class CuaNativeTests: XCTestCase {
    private func binary() throws -> URL {
        guard ProcessInfo.processInfo.environment["LINA_CUA_NATIVE_TESTS"] == "1" else {
            throw XCTSkip("set LINA_CUA_NATIVE_TESTS=1 after make app for native signature checks")
        }
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent()
        let binary = root.appendingPathComponent("build/LINA Helper.app/Contents/Helpers/cua-driver")
        XCTAssertTrue(FileManager.default.isExecutableFile(atPath: binary.path))
        return binary
    }

    private func suspended(_ binary: URL, body: (pid_t) throws -> Void) throws {
        let child = try CuaProcess.spawn(CuaLaunch(executable: binary.path, arguments: ["--version"],
            environment: ["PATH": "/usr/bin:/bin", "CUA_DRIVER_EMBEDDED": "1", "CUA_DRIVER_RS_TELEMETRY_ENABLED": "0"]))
        defer { kill(child, SIGKILL); _ = CuaProcess.reap(child, wait: true) }
        XCTAssertFalse(CuaProcess.reap(child, wait: false), "spawn must leave the child suspended")
        try body(child)
    }

    func testUpstreamGuestPassesBeforeResume() throws {
        try suspended(binary()) { child in try CuaProcess.verify(child) }
    }

    func testResignedCopyIsRejectedBeforeResume() throws {
        let original = try binary()
        let copy = original.deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("cua-native-resigned-" + UUID().uuidString)
        try FileManager.default.copyItem(at: original, to: copy)
        defer { try? FileManager.default.removeItem(at: copy) }
        let signing = Process()
        signing.executableURL = URL(fileURLWithPath: "/usr/bin/codesign")
        signing.arguments = ["--force", "--sign", "-", copy.path]
        try signing.run(); signing.waitUntilExit()
        XCTAssertEqual(signing.terminationStatus, 0)
        try suspended(copy) { child in XCTAssertThrowsError(try CuaProcess.verify(child)) }
    }

    func testModifiedExecutedPageIsKilledByKernel() throws {
        let original = try binary()
        let build = original.deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent()
        let copy = build.appendingPathComponent("cua-native-page-" + UUID().uuidString)
        var bytes = try Data(contentsOf: original)
        func integer(_ offset: Int, _ count: Int, big: Bool = false) -> UInt64 {
            let values = Array(bytes[offset..<offset + count])
            return (big ? values : values.reversed()).reduce(0) { ($0 << 8) | UInt64($1) }
        }
        var slice = 0
        if integer(0, 4, big: true) == 0xcafebabe {
            for i in 0..<Int(integer(4, 4, big: true)) {
                let index = 8 + i * 20
                if integer(index, 4, big: true) == 0x0100000c { slice = Int(integer(index + 8, 4, big: true)); break }
            }
        }
        XCTAssertEqual(integer(slice, 4), 0xfeedfacf)
        var command = slice + 32
        var entry: Int?
        for _ in 0..<Int(integer(slice + 16, 4)) {
            if integer(command, 4) == 0x80000028 { entry = slice + Int(integer(command + 8, 8)); break }
            command += Int(integer(command + 4, 4))
        }
        bytes[try XCTUnwrap(entry)] ^= 1
        try bytes.write(to: copy)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: copy.path)
        defer { try? FileManager.default.removeItem(at: copy) }
        let pid = try CuaProcess.spawn(CuaLaunch(executable: copy.path, arguments: ["--version"], environment: ["PATH": "/usr/bin:/bin"]))
        var reaped = false
        defer { if !reaped { kill(pid, SIGKILL); _ = CuaProcess.reap(pid, wait: true) } }
        // The identity and CDHash are unchanged. The executed page's integrity is
        // enforced by the kernel at page-in, not by the suspended identity check.
        try CuaProcess.verify(pid)
        XCTAssertEqual(kill(pid, SIGCONT), 0)
        let deadline = ProcessInfo.processInfo.systemUptime + 3
        while ProcessInfo.processInfo.systemUptime < deadline {
            var status: Int32 = 0
            if waitpid(pid, &status, WNOHANG) == pid {
                reaped = true
                XCTAssertEqual(status & 0x7f, SIGKILL)
                return
            }
            Thread.sleep(forTimeInterval: 0.01)
        }
        XCTFail("kernel did not kill the modified executable page")
    }
}
