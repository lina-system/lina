import Foundation
import ApplicationServices
import AppKit

/// Elements stay on one action worker. Children are fetched individually so a
/// hostile or very large application cannot defeat max_nodes by returning an array.
protocol AccessibilityElement {
    func snapshot() throws -> [String: JSONValue]
    func childCount() throws -> Int
    func child(at index: Int) throws -> (any AccessibilityElement)?
}

enum AccessibilityTree {
    // Root is the application at "0"; every suffix is a zero-based child index.
    static func read(_ root: any AccessibilityElement, depth: Int, maxNodes: Int,
                     context: ActionContext) throws -> [[String: JSONValue]] {
        var result: [[String: JSONValue]] = []
        func visit(_ element: any AccessibilityElement, path: String, level: Int) throws {
            try context.checkDeadline()
            guard result.count < maxNodes else { return }
            var values = try element.snapshot()
            for key in ["role", "subrole", "title", "value"] {
                if let text = values[key]?.string { values[key] = .string(String(text.prefix(512))) }
                else if values[key] == nil { values[key] = .null }
            }
            values["frame"] = values["frame"] ?? .null
            values["enabled"] = values["enabled"] ?? .null
            values["path"] = .string(path)
            result.append(values)
            guard level < depth, result.count < maxNodes else { return }
            let count = try element.childCount()
            for index in 0..<max(0, count) {
                try context.checkDeadline()
                guard result.count < maxNodes else { break }
                if let child = try element.child(at: index) {
                    try visit(child, path: path + "/\(index)", level: level + 1)
                }
            }
        }
        try visit(root, path: "0", level: 0)
        return result
    }

}

/// How an AX read error is treated. Apps return kAXErrorFailure for attributes they cannot
/// produce right now (Finder does for several), so a failure means "no value" rather than
/// failing the whole tree; a disabled API or a dead element still surfaces.
enum AXReadOutcome: Equatable {
    case value, absent, error(ErrorCode)
    static func classify(_ error: AXError) -> AXReadOutcome {
        switch error {
        case .success: return .value
        case .attributeUnsupported, .noValue, .failure: return .absent
        case .apiDisabled: return .error(.permissionMissing)
        case .invalidUIElement, .cannotComplete, .illegalArgument: return .error(.targetNotFound)
        default: return .error(.internalError)
        }
    }
}

struct SystemAccessibilityElement: AccessibilityElement {
    let element: AXUIElement
    static func application(_ app: String, context: ActionContext) throws -> SystemAccessibilityElement {
        let completion = ActionCompletion<Int32>()
        DispatchQueue.main.async {
            do {
                try context.checkDeadline()
                let running = app == "frontmost" ? NSWorkspace.shared.frontmostApplication : NSRunningApplication.runningApplications(withBundleIdentifier: app).first
                guard let running, !running.isTerminated else { throw HelperError(.targetNotFound, "application not running") }
                completion.finish(.success(running.processIdentifier))
            } catch { completion.finish(.failure((error as? HelperError) ?? HelperError(.internalError, "application lookup failed"))) }
        }
        let root = AXUIElementCreateApplication(try completion.wait(context))
        // Bound each synchronous AX IPC as well as the dispatcher's total deadline.
        AXUIElementSetMessagingTimeout(root, Float(min(1, max(0.01, context.deadline - ProcessInfo.processInfo.systemUptime))))
        return Self(element: root)
    }
    private func attribute(_ name: String) throws -> CFTypeRef? {
        var value: CFTypeRef?
        let error = AXUIElementCopyAttributeValue(element, name as CFString, &value)
        if AXReadOutcome.classify(error) == .absent { return nil }
        try check(error)
        return value
    }
    private func check(_ error: AXError) throws {
        switch error {
        case .success: return
        case .apiDisabled: throw HelperError(.permissionMissing, "accessibility")
        case .invalidUIElement, .cannotComplete, .illegalArgument: throw HelperError(.targetNotFound, "AX target unavailable")
        default: throw HelperError(.internalError, "accessibility operation failed (AXError \(error.rawValue))")
        }
    }
    func snapshot() throws -> [String: JSONValue] {
        var result: [String: JSONValue] = [:]
        for (key, attributeName) in [("role", kAXRoleAttribute), ("subrole", kAXSubroleAttribute),
                                     ("title", kAXTitleAttribute), ("value", kAXValueAttribute)] {
            let value = try attribute(attributeName)
            if let text = value as? String { result[key] = .string(String(text.prefix(512))) }
            else if let number = value as? NSNumber { result[key] = .string(String(number.stringValue.prefix(512))) }
            else { result[key] = .null }
        }
        result["enabled"] = (try attribute(kAXEnabledAttribute) as? NSNumber).map { .bool($0.boolValue) } ?? .null
        var point = CGPoint.zero, size = CGSize.zero
        if let position = try attribute(kAXPositionAttribute), CFGetTypeID(position) == AXValueGetTypeID(),
           let dimensions = try attribute(kAXSizeAttribute), CFGetTypeID(dimensions) == AXValueGetTypeID(),
           AXValueGetValue(position as! AXValue, .cgPoint, &point), AXValueGetValue(dimensions as! AXValue, .cgSize, &size),
           [point.x, point.y, size.width, size.height].allSatisfy({ $0.isFinite }) {
            result["frame"] = .object(["x": .number(point.x), "y": .number(point.y), "width": .number(size.width), "height": .number(size.height)])
        } else { result["frame"] = .null }
        return result
    }
    func childCount() throws -> Int {
        var count = 0
        let error = AXUIElementGetAttributeValueCount(element, kAXChildrenAttribute as CFString, &count)
        if AXReadOutcome.classify(error) == .absent { return 0 }
        try check(error)
        return count
    }
    func child(at index: Int) throws -> (any AccessibilityElement)? {
        guard index >= 0, index < (try childCount()) else { return nil }
        var values: CFArray?
        let error = AXUIElementCopyAttributeValues(element, kAXChildrenAttribute as CFString, index, 1, &values)
        if error == .illegalArgument || error == .invalidUIElement || error == .failure { return nil }
        try check(error)
        guard let values, CFArrayGetCount(values) == 1 else { return nil }
        let value = unsafeBitCast(CFArrayGetValueAtIndex(values, 0), to: CFTypeRef.self)
        guard CFGetTypeID(value) == AXUIElementGetTypeID() else { return nil }
        return Self(element: value as! AXUIElement)
    }
}
