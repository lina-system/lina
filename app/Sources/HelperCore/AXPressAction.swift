import Foundation

struct AXPressArguments {
    let app: String
    let path: AXPath
    let pathString: String
    let expectRole: String?
    init(_ context: ActionContext) throws {
        try ActionArguments.keys(context, ["app", "path", "expect_role"])
        app = try ActionArguments.app(context.args["app"])
        pathString = try ActionArguments.string(context.args["path"], name: "path")
        path = try AXPath(pathString)
        expectRole = try context.args["expect_role"].map { try ActionArguments.string($0, name: "expect_role") }
    }
}

enum AXPressAction {
    static func make(probe: @escaping @Sendable () -> [String: JSONValue]) -> Action {
        Action { context in
            try ActionArguments.grant("accessibility", probe: probe)
            let args = try AXPressArguments(context)
            let root = try SystemAccessibilityElement.application(args.app, context: context)
            var result = try AccessibilityTree.press(root, path: args.path, expectRole: args.expectRole, context: context)
            result["path"] = .string(args.pathString)
            return result
        }
    }
}
