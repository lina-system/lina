import Foundation
import AppKit

struct AppOpenArguments {
    let bundleID: String
    init(_ context: ActionContext) throws {
        try ActionArguments.keys(context, ["bundle_id"])
        bundleID = try ActionArguments.bundleID(context.args["bundle_id"])
    }
}

enum AppOpenAction {
    static func make() -> Action {
        Action { context in
            // NSWorkspace launch/activation requires no privacy grant (design §6).
            let args = try AppOpenArguments(context)
            let completion = ActionCompletion<Int32>()
            DispatchQueue.main.async {
                do {
                    try context.checkDeadline()
                    guard let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: args.bundleID) else {
                        throw HelperError(.targetNotFound, "application not found")
                    }
                    let config = NSWorkspace.OpenConfiguration()
                    config.activates = true
                    try context.checkDeadline()
                    NSWorkspace.shared.openApplication(at: url, configuration: config) { app, error in
                        if let app, error == nil { completion.finish(.success(app.processIdentifier)) }
                        else { completion.finish(.failure(HelperError(.internalError, "application launch failed"))) }
                    }
                } catch { completion.finish(.failure((error as? HelperError) ?? HelperError(.internalError, "application launch failed"))) }
            }
            return ["pid": .number(Double(try completion.wait(context)))]
        }
    }
}
