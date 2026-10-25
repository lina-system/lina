import XCTest
@testable import HelperCore

private final class FakeElement: AccessibilityElement {
    let role: String
    var children: [FakeElement]
    var presses = 0
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
    func press() throws { presses += 1 }
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
    func testPressResolvesFreshAndChecksRole() throws {
        let old = FakeElement("AXButton"), replacement = FakeElement("AXTextField")
        let root = FakeElement("AXApplication", [old])
        let path = try AXPath("0/0")
        try AccessibilityTree.press(root, path: path, expectRole: "AXButton", context: context)
        XCTAssertEqual(old.presses, 1)
        root.children = [replacement]
        XCTAssertThrowsError(try AccessibilityTree.press(root, path: path, expectRole: "AXButton", context: context)) {
            XCTAssertEqual(($0 as? HelperError)?.code, .targetNotFound)
        }
        XCTAssertEqual(replacement.presses, 0)
        root.children = []
        XCTAssertThrowsError(try AccessibilityTree.press(root, path: path, expectRole: nil, context: context)) {
            XCTAssertEqual(($0 as? HelperError)?.code, .targetNotFound)
        }
    }
    func testExpiredWorkDoesNotPress() throws {
        let root = FakeElement("AXButton")
        let expired = ActionContext(args: [:], limits: [:], deadline: 0)
        XCTAssertThrowsError(try AccessibilityTree.press(root, path: AXPath("0"), expectRole: nil, context: expired))
        XCTAssertThrowsError(try AccessibilityTree.read(root, depth: 8, maxNodes: 100, context: expired))
        XCTAssertEqual(root.presses, 0)
    }
}
