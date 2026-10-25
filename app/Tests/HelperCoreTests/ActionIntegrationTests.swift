import XCTest
import Darwin
@testable import HelperCore

final class ActionIntegrationTests: XCTestCase {
    func testPolicyTimeoutReachesContextAndEnforcesDeadline() {
        let observed = Locked<Double>(0)
        let release = DispatchSemaphore(value: 0)
        defer { release.signal() }
        let dispatcher = ActionDispatcher(actions: ["screenshot": Action(timeout: 15) { context in
            observed.withLock { $0 = context.deadline - ProcessInfo.processInfo.systemUptime }
            _ = release.wait(timeout: .now() + 2)
            try context.checkDeadline()
            return [:]
        }])
        let policy = Policy.parse(Data(#"{"actions":{"screenshot":{"timeout_s":0.2}}}"#.utf8), owner: 0, mode: 0o644)
        XCTAssertEqual(dispatcher.dispatch(Request(action: "screenshot"), policy: policy).error?.code, .timeout)
        XCTAssertGreaterThan(observed.withLock { $0 }, 0)
        XCTAssertLessThanOrEqual(observed.withLock { $0 }, 0.2)
    }
    func testLargeTreeResponseUsesSeparateFrameLimit() throws {
        let data = try Response.success(id: "tree", result: ["elements": .array((0..<2000).map { _ in
            .object(["title": .string(String(repeating: "a", count: 512))])
        })]).line()
        var requestFrame = LineFrame()
        XCTAssertThrowsError(try requestFrame.append(data))
        var responseFrame = LineFrame(maximumBytes: Transport.responseLimit)
        XCTAssertEqual(try responseFrame.append(data), data.dropLast())
    }
    func testTimedOutFrameworkCallbackRetainsWorkerCapacity() {
        let completion = ActionCompletion<Int>()
        let dispatcher = ActionDispatcher(actions: ["status": Action(timeout: 0.02) { context in
            _ = try completion.wait(context)
            return [:]
        }], capacity: 1)
        XCTAssertEqual(dispatcher.dispatch(Request(action: "status"), policy: .restricted).error?.code, .timeout)
        Thread.sleep(forTimeInterval: 0.03)
        XCTAssertEqual(dispatcher.dispatch(Request(action: "status"), policy: .restricted).error?.code, .busy)
        completion.finish(.success(1))
    }

}
