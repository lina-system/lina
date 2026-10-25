import XCTest
@testable import HelperCore

final class DesktopActionTests: XCTestCase {
    func testNewActionsAreRegisteredAndPolicyGated() {
        let actions = SystemActions.registry(home: NSTemporaryDirectory(), probe: { [:] })
        let dispatcher = ActionDispatcher(actions: actions)
        for name in ["screenshot", "ax.tree", "ax.press", "input.click", "input.keys", "app.open"] {
            XCTAssertNotNil(actions[name], name)
            XCTAssertEqual(dispatcher.dispatch(Request(action: name), policy: .restricted).error?.code, .notAllowed, name)
        }
    }

    func testGrantIsCheckedBeforeArguments() {
        let actions = SystemActions.registry(home: NSTemporaryDirectory(), probe: { [:] })
        for (name, grant) in [("screenshot", "screen_recording"), ("ax.tree", "accessibility"),
                              ("ax.press", "accessibility"), ("input.click", "accessibility"), ("input.keys", "accessibility")] {
            let policy = Policy.parse(try! JSONEncoder().encode(["actions": [name: [String: String]()]]), owner: 0, mode: 0o644)
            let response = ActionDispatcher(actions: actions).dispatch(Request(action: name, args: ["unknown": .bool(true)]), policy: policy)
            XCTAssertEqual(response.error?.code, .permissionMissing, name)
            XCTAssertEqual(response.error?.detail, grant, name)
        }
    }
    func testUnknownKeysRejectedByEveryHandlerWithoutCallingOS() throws {
        let actions = SystemActions.registry(home: NSTemporaryDirectory(), probe: {
            ["accessibility": .bool(true), "screen_recording": .bool(true)]
        })
        for name in ["screenshot", "ax.tree", "ax.press", "input.click", "input.keys", "app.open"] {
            let context = ActionContext(args: ["unknown": .null], limits: [:], deadline: ProcessInfo.processInfo.systemUptime + 1)
            XCTAssertThrowsError(try XCTUnwrap(actions[name]).run(context)) {
                XCTAssertEqual(($0 as? HelperError)?.code, .badRequest, name)
            }
        }
    }

}
