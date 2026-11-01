import XCTest
@testable import HelperCore

private final class FakeElement: AccessibilityElement {
    let role: String
    var children: [FakeElement]
    var requestedChildren = 0
    init(_ role: String, _ children: [FakeElement] = []) { self.role = role; self.children = children }
    func snapshot() throws -> [String: JSONValue] {
        ["role": .string(role), "title": .string(String(repeating: "🙂", count: 600)), "value": .string(String(repeating: "v", count: 600))]
    }
    func childCount() throws -> Int { children.count }
    func child(at index: Int) throws -> (any AccessibilityElement)? {
        requestedChildren += 1
        return children.indices.contains(index) ? children[index] : nil
    }
}

final class AccessibilityTreeTests: XCTestCase {
    private var context: ActionContext { ActionContext(args: [:], limits: [:], deadline: ProcessInfo.processInfo.systemUptime + 10) }
    func testTraversalLimitsAndStablePaths() throws {
        let button = FakeElement("AXButton")
        let root = FakeElement("AXApplication", [FakeElement("AXWindow", [button]), FakeElement("AXWindow")])
        let nodes = try AccessibilityTree.read(root, depth: 8, maxNodes: 2000, context: context)
        XCTAssertEqual(nodes.compactMap { $0["path"]?.string }, ["0", "0/0", "0/0/0", "0/1"])
        XCTAssertEqual(nodes.last?["role"], .string("AXWindow"))
        XCTAssertEqual(nodes.first?["title"]?.string?.count, 512)
        XCTAssertEqual(nodes.first?["value"]?.string?.count, 512)
        XCTAssertEqual(try AccessibilityTree.read(root, depth: 0, maxNodes: 10, context: context).count, 1)
        XCTAssertEqual(try AccessibilityTree.read(root, depth: 1, maxNodes: 10, context: context).count, 3)
        root.requestedChildren = 0
        XCTAssertEqual(try AccessibilityTree.read(root, depth: 8, maxNodes: 1, context: context).count, 1)
        XCTAssertEqual(root.requestedChildren, 0)
        XCTAssertEqual(try AccessibilityTree.read(root, depth: 8, maxNodes: 2, context: context).count, 2)
    }
    func testFailureFromAnAttributeIsAbsentNotFatal() {
        // Finder answers kAXErrorFailure (-25200) for some attributes; that must not fail ax.tree.
        XCTAssertEqual(AXReadOutcome.classify(.failure), .absent)
        XCTAssertEqual(AXReadOutcome.classify(.noValue), .absent)
        XCTAssertEqual(AXReadOutcome.classify(.apiDisabled), .error(.permissionMissing))
        XCTAssertEqual(AXReadOutcome.classify(.invalidUIElement), .error(.targetNotFound))
        XCTAssertEqual(AXReadOutcome.classify(.notImplemented), .error(.internalError))
    }
    func testExpiredWorkDoesNotRead() throws {
        let root = FakeElement("AXButton")
        let expired = ActionContext(args: [:], limits: [:], deadline: 0)
        XCTAssertThrowsError(try AccessibilityTree.read(root, depth: 8, maxNodes: 100, context: expired))
    }
}
