import XCTest
@testable import HelperCore

@MainActor
final class ActionTests: XCTestCase {
    func testStatusAndPermissionsWithInjectedGrants() async throws {
        let home = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: home, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: home) }
        let opened = Locked<[String]>([])
        let registry = SystemActions.registry(home: home.path, probe: {
            ["accessibility": .bool(true), "screen_recording": .bool(true)]
        }, openPane: { url in
            opened.withLock { $0.append(url.absoluteString) }
            return true
        })
        let dispatcher = ActionDispatcher(actions: registry)
        let status = await Task.detached { dispatcher.dispatch(Request(action: "status"), policy: .restricted) }.value
        XCTAssertTrue(status.ok)
        XCTAssertEqual(status.result?["version"], .string(SystemActions.version))
        XCTAssertEqual(status.result?["pid"], .number(Double(ProcessInfo.processInfo.processIdentifier)))
        XCTAssertNotNil(status.result?["uptime"])
        XCTAssertEqual(status.result?["grants"]?.object?["accessibility"], .bool(true))
        XCTAssertNotNil(status.result?["screen_recording"]?.object?["next_alert"]?.string)
        for pane in ["accessibility", "screen_recording"] {
            let response = await Task.detached {
                dispatcher.dispatch(Request(action: "permissions", args: ["open": .string(pane)]), policy: .restricted)
            }.value
            XCTAssertTrue(response.ok)
            XCTAssertEqual(response.result?["screen_recording"], .bool(true))
        }
        XCTAssertEqual(opened.withLock { $0 }, [
            "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility",
            "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture",
        ])
        for request in [Request(action: "permissions", args: ["open": .string("other")]),
                        Request(action: "permissions", args: ["open": .bool(true)]),
                        Request(action: "status", args: ["unused": .bool(true)])] {
            let response = await Task.detached { dispatcher.dispatch(request, policy: .restricted) }.value
            XCTAssertEqual(response.error?.code, .badRequest)
        }
        let restarted = ActionDispatcher(actions: SystemActions.registry(home: home.path, probe: {
            ["accessibility": .bool(true), "screen_recording": .bool(true)]
        }))
        let afterRestart = await Task.detached { restarted.dispatch(Request(action: "status"), policy: .restricted) }.value
        XCTAssertEqual(afterRestart.result?["screen_recording"], status.result?["screen_recording"])
    }

    func testActionErrorsUseProtocolCodes() async {
        for code in [ErrorCode.permissionMissing, .targetNotFound, .internalError] {
            let dispatcher = ActionDispatcher(actions: ["status": Action { _ in throw HelperError(code, "test") }])
            let response = await Task.detached { dispatcher.dispatch(Request(action: "status"), policy: .restricted) }.value
            XCTAssertEqual(response.error?.code, code)
        }
    }
}
