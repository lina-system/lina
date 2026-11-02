import XCTest
import Darwin
@testable import HelperCore

final class CLITests: XCTestCase {
    func testFileStreamAndLastStderrLine() throws {
        let home = "/tmp/lh-cli-" + UUID().uuidString
        defer { try? FileManager.default.removeItem(atPath: home) }
        try FileManager.default.createDirectory(atPath: home + "/run", withIntermediateDirectories: true)
        let path = home + "/run/helper.sock"
        let listener = socket(AF_UNIX, SOCK_STREAM, 0)
        XCTAssertGreaterThanOrEqual(listener, 0)
        defer { close(listener) }
        let bound = try Transport.withAddress(path) { Darwin.bind(listener, $0, $1) }
        if bound != 0 && errno == EPERM { throw XCTSkip("environment prohibits Unix listener bind (EPERM)") }
        XCTAssertEqual(bound, 0)
        XCTAssertEqual(listen(listener, 1), 0)
        let bytes = Data([137, 80, 78, 71, 13, 10, 26, 10]) + Data(repeating: 0xff, count: 130_000)
        let screenshot = home + "/fixture.png"
        try bytes.write(to: URL(fileURLWithPath: screenshot))
        let done = DispatchSemaphore(value: 0)
        let failure = Locked<String?>(nil)
        DispatchQueue.global().async {
            defer { done.signal() }
            do {
                try Transport.wait(listener, events: Int16(POLLIN), deadline: ProcessInfo.processInfo.systemUptime + 5)
                let client = accept(listener, nil, nil)
                defer { close(client) }
                try Transport.prepare(client)
                let request = try Request.decode(Transport.readLine(client))
                let response = Response.success(id: request.id, result: ["path": .string(screenshot), "width": .number(1), "height": .number(1)])
                try Transport.write(response.line(), to: client)
            } catch { failure.withLock { $0 = String(describing: error) } }
        }
        // Both SwiftPM build layouts put the executable beside the test bundle.
        let executable = Bundle(for: Self.self).bundleURL.deletingLastPathComponent().appendingPathComponent("lina-helperctl")
        XCTAssertTrue(FileManager.default.isExecutableFile(atPath: executable.path))
        let stdoutURL = URL(fileURLWithPath: home + "/stdout"), stderrURL = URL(fileURLWithPath: home + "/stderr")
        FileManager.default.createFile(atPath: stdoutURL.path, contents: nil)
        FileManager.default.createFile(atPath: stderrURL.path, contents: nil)
        let stdout = try FileHandle(forWritingTo: stdoutURL), stderr = try FileHandle(forWritingTo: stderrURL)
        defer { try? stdout.close(); try? stderr.close() }
        let process = Process()
        process.executableURL = executable
        process.arguments = ["screenshot", "--out", "-"]
        process.environment = ProcessInfo.processInfo.environment.merging(["LINA_HELPER_HOME": home]) { _, new in new }
        process.standardOutput = stdout; process.standardError = stderr
        try process.run(); process.waitUntilExit()
        XCTAssertEqual(done.wait(timeout: .now() + 6), .success)
        XCTAssertNil(failure.withLock { $0 })
        XCTAssertEqual(process.terminationStatus, 0)
        XCTAssertEqual(try Data(contentsOf: stdoutURL), bytes)
        let lines = try String(contentsOf: stderrURL, encoding: .utf8).split(separator: "\n")
        let response = try JSONDecoder().decode(Response.self, from: Data(try XCTUnwrap(lines.last).utf8))
        XCTAssertTrue(response.ok)
        XCTAssertEqual(response.result?["path"], .string(screenshot))
    }
}
