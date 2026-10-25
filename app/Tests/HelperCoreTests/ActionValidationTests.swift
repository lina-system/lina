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
    func testTreeLimitsAndPaths() throws {
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
        XCTAssertEqual(try AXPath("0/3/1").indices, [3, 1])
        XCTAssertEqual(try AXPath("0").indices, [])
        for path in ["", "/0", "0/", "1", "0/-1", "0/01", "0/a", "0//1", "0/99999999999999999999999"] {
            bad { _ = try AXPath(path) }
        }
        bad { _ = try AXPressArguments(context(["app": .string("frontmost"), "path": .string("0"), "expect_role": .number(1)])) }
    }
    func testClickArguments() throws {
        let args: [String: JSONValue] = ["x": .number(-100.5), "y": .number(20)]
        let click = try ClickArguments(context(args))
        XCTAssertEqual(click.button, "left"); XCTAssertEqual(click.count, 1)
        for extra: [String: JSONValue] in [["button": .string("middle")], ["count": .number(3)],
            ["count": .number(1.2)], ["x": .string("1")], ["y": .number(.infinity)], ["z": .number(0)]] {
            bad { _ = try ClickArguments(context(args.merging(extra) { _, new in new })) }
        }
        bad { _ = try ClickArguments(context([:])) }
    }
    func testKeysAndBundleArguments() throws {
        _ = try KeysArguments(context(["text": .string(String(repeating: "🙂", count: 512))]))
        for key in ["return", "tab", "escape", "space", "delete", "up", "down", "left", "right", "home", "end", "pageup", "pagedown"] + (1...12).map({ "f\($0)" }) {
            _ = try KeysArguments(context(["key": .string(key), "modifiers": .array([.string("cmd"), .string("shift")])]))
        }
        for args: [String: JSONValue] in [[:], ["text": .string(String(repeating: "a", count: 513))],
            ["text": .string("x"), "key": .string("return")], ["key": .string("a")],
            ["key": .string("f13")], ["key": .string("tab"), "modifiers": .array([.string("bad")])],
            ["key": .string("tab"), "modifiers": .string("cmd")],
            ["key": .string("tab"), "modifiers": .array([.string("cmd"), .string("cmd")])],
            ["text": .string("x"), "modifiers": .array([])], ["text": .string("x"), "extra": .null]] {
            bad { _ = try KeysArguments(context(args)) }
        }
        XCTAssertEqual(try AppOpenArguments(context(["bundle_id": .string("com.example.Test")] )).bundleID, "com.example.Test")
        for args: [String: JSONValue] in [[:], ["bundle_id": .string("")], ["bundle_id": .string("/tmp/test.app")],
            ["bundle_id": .string("com..test")], ["bundle_id": .string("com.example.Test"), "extra": .null]] {
            bad { _ = try AppOpenArguments(context(args)) }
        }
    }
}
