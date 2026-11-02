import Foundation

struct AXTreeArguments {
    let app: String
    let depth: Int
    let maxNodes: Int
    init(_ context: ActionContext) throws {
        try ActionArguments.keys(context, ["app", "depth", "max_nodes"])
        app = try ActionArguments.app(context.args["app"])
        // Hard ceilings protect stack and response memory even with malformed policy.
        let depthLimit = try ActionArguments.integer(context.limits["max_depth"] ?? .number(8), name: "max_depth", range: 0...64)
        let nodeLimit = try ActionArguments.integer(context.limits["max_nodes"] ?? .number(2000), name: "max_nodes", range: 1...10000)
        depth = try ActionArguments.integer(context.args["depth"] ?? .number(Double(depthLimit)), name: "depth", range: 0...depthLimit)
        maxNodes = try ActionArguments.integer(context.args["max_nodes"] ?? .number(Double(nodeLimit)), name: "max_nodes", range: 1...nodeLimit)
    }
}

enum AXTreeAction {
    static func make(probe: @escaping @Sendable () -> [String: JSONValue]) -> Action {
        Action { context in
            try ActionArguments.grant("accessibility", probe: probe)
            let args = try AXTreeArguments(context)
            let root = try SystemAccessibilityElement.application(args.app, context: context)
            let elements = try AccessibilityTree.read(root, depth: args.depth, maxNodes: args.maxNodes, context: context)
            return ["elements": .array(elements.map { .object($0) })]
        }
    }
}
