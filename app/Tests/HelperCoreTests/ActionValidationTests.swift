import XCTest
@testable import HelperCore

final class ActionValidationTests: XCTestCase {
    private func context(_ args: [String: JSONValue], limits: [String: JSONValue] = [:]) -> ActionContext {
        ActionContext(args: args, limits: limits, deadline: ProcessInfo.processInfo.systemUptime + 10)
    }
    private func bad(_ body: () throws -> Void, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertThrowsError(try body(), file: file, line: line) {
            XCTAssertEqual(($0 as? HelperError)?.code, .badRequest, file: file, line: line)
        }
    }
    func testScreenshotArguments() throws {
        XCTAssertEqual(try ScreenshotArguments(context([:])).target, .main)
        XCTAssertEqual(try ScreenshotArguments(context(["display": .number(42)])).target, .display(42))
        XCTAssertEqual(try ScreenshotArguments(context(["window": .number(5)])).target, .window(5))
        for args: [String: JSONValue] in [["display": .string("other")], ["window": .number(0)],
            ["display": .number(-1)], ["window": .number(1.2)], ["display": .number(4294967296)],
            ["display": .string("main"), "window": .number(1)], ["unused": .bool(true)]] {
            bad { _ = try ScreenshotArguments(context(args)) }
        }
    }
    func testTreeLimits() throws {
        let args: [String: JSONValue] = ["app": .string("frontmost")]
        let defaults = try AXTreeArguments(context(args))
        XCTAssertEqual(defaults.depth, 8); XCTAssertEqual(defaults.maxNodes, 2000)
        let limits: [String: JSONValue] = ["max_depth": .number(2), "max_nodes": .number(3)]
        XCTAssertEqual(try AXTreeArguments(context(args, limits: limits)).depth, 2)
        XCTAssertEqual(try AXTreeArguments(context(args, limits: limits)).maxNodes, 3)
        for extra: [String: JSONValue] in [["depth": .number(3)], ["max_nodes": .number(4)],
            ["depth": .number(-1)], ["max_nodes": .number(0)], ["depth": .bool(true)],
            ["depth": .number(1.5)], ["extra": .null]] {
            bad { _ = try AXTreeArguments(context(args.merging(extra) { _, new in new }, limits: limits)) }
        }
    }
    func testTreeAppBundleArguments() throws {
        XCTAssertEqual(try AXTreeArguments(context(["app": .string("com.example.Test")])).app, "com.example.Test")
        for args: [String: JSONValue] in [[:], ["app": .string("")], ["app": .string("/tmp/test.app")],
            ["app": .string("com..test")], ["app": .string("com.example.Test"), "extra": .null]] {
            bad { _ = try AXTreeArguments(context(args)) }
        }
    }
}
