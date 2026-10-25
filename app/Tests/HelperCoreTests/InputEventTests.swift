import XCTest
import CoreGraphics
@testable import HelperCore

final class InputEventTests: XCTestCase {
    private func context(_ args: [String: JSONValue]) -> ActionContext {
        ActionContext(args: args, limits: [:], deadline: ProcessInfo.processInfo.systemUptime + 5)
    }
    private let grants: @Sendable () -> [String: JSONValue] = { ["accessibility": .bool(true)] }
    func testLongUnicodeTextSurvivesEventEncoding() throws {
        let text = String(repeating: "🙂e\u{301}", count: 256)
        let actual = Locked("")
        let action = InputKeysAction.make(probe: grants, post: { events in
            for event in events where event.type == .keyDown {
                var units = [UniChar](repeating: 0, count: 2048)
                var count = 0
                event.keyboardGetUnicodeString(maxStringLength: units.count, actualStringLength: &count, unicodeString: &units)
                actual.withLock { $0 += String(decoding: units.prefix(count), as: UTF16.self) }
            }
            XCTAssertEqual(events.filter { $0.type == .keyDown }.count, events.filter { $0.type == .keyUp }.count)
        })
        _ = try action.run(context(["text": .string(text)]))
        XCTAssertEqual(actual.withLock { $0 }, text)
    }
    func testNamedKeyModifiersAndMouseSequence() throws {
        let keyboard = InputKeysAction.make(probe: grants, post: { events in
            XCTAssertEqual(events.map(\.type), [.keyDown, .keyUp])
            for event in events {
                XCTAssertEqual(event.getIntegerValueField(.keyboardEventKeycode), 48)
                XCTAssertEqual(event.flags, [.maskCommand, .maskShift])
            }
        })
        _ = try keyboard.run(context(["key": .string("tab"), "modifiers": .array([.string("cmd"), .string("shift")])]))
        let mouse = InputClickAction.make(probe: grants, post: { events in
            XCTAssertEqual(events.map(\.type), [.rightMouseDown, .rightMouseUp, .rightMouseDown, .rightMouseUp])
            XCTAssertEqual(events.map { $0.getIntegerValueField(.mouseEventClickState) }, [1, 1, 2, 2])
            XCTAssertTrue(events.allSatisfy { $0.location == CGPoint(x: -10.5, y: 200) })
        })
        _ = try mouse.run(context(["x": .number(-10.5), "y": .number(200), "button": .string("right"), "count": .number(2)]))
    }
    func testEmptyTextDoesNotPostAKey() throws {
        let called = Locked(false)
        let action = InputKeysAction.make(probe: grants, post: { _ in called.withLock { $0 = true } })
        _ = try action.run(context(["text": .string("")]))
        XCTAssertFalse(called.withLock { $0 })
    }
}
