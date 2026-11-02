import XCTest
@testable import HelperCore

final class OutboxTests: XCTestCase {
    func testRetentionAndPrivateModes() throws {
        let home = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: home) }
        let outbox = Outbox(home: home.path)
        let first = try outbox.writePNG(Data([1, 2, 3]))
        let second = try outbox.writePNG(Data([4]))
        XCTAssertNotEqual(first, second)
        let now = Date()
        try FileManager.default.setAttributes([.modificationDate: now.addingTimeInterval(-601)], ofItemAtPath: first)
        try FileManager.default.setAttributes([.modificationDate: now.addingTimeInterval(-599)], ofItemAtPath: second)  // not exactly 600: mtime keeps whole seconds only
        try outbox.cleanup(now: now)
        XCTAssertFalse(FileManager.default.fileExists(atPath: first))
        XCTAssertTrue(FileManager.default.fileExists(atPath: second))
        XCTAssertEqual(try Data(contentsOf: URL(fileURLWithPath: second)), Data([4]))
        for (path, mode) in [(home.appendingPathComponent("outbox").path, 0o700), (second, 0o600)] {
            let attrs = try FileManager.default.attributesOfItem(atPath: path)
            XCTAssertEqual((attrs[.posixPermissions] as? NSNumber)?.intValue, mode)
        }
        try outbox.cleanup(now: now.addingTimeInterval(3600))
        XCTAssertFalse(FileManager.default.fileExists(atPath: second))
    }
    func testOutboxRefusesSymlinkAndDoesNotTraverseChildren() throws {
        let home = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: home) }
        try FileManager.default.createDirectory(at: home, withIntermediateDirectories: true)
        let target = home.appendingPathComponent("target")
        try FileManager.default.createDirectory(at: target, withIntermediateDirectories: true)
        let path = home.appendingPathComponent("outbox")
        try FileManager.default.createSymbolicLink(at: path, withDestinationURL: target)
        let outbox = Outbox(home: home.path)
        XCTAssertThrowsError(try outbox.writePNG(Data()))
        XCTAssertThrowsError(try outbox.cleanup())
        try FileManager.default.removeItem(at: path)
        let file = try outbox.writePNG(Data())
        let nested = path.appendingPathComponent("nested")
        try FileManager.default.createDirectory(at: nested, withIntermediateDirectories: true)
        try FileManager.default.createSymbolicLink(at: path.appendingPathComponent("link"), withDestinationURL: target)
        try outbox.cleanup(now: Date().addingTimeInterval(3600))
        XCTAssertFalse(FileManager.default.fileExists(atPath: file))
        XCTAssertTrue(FileManager.default.fileExists(atPath: nested.path))
        XCTAssertTrue(FileManager.default.fileExists(atPath: target.path))
    }
}
