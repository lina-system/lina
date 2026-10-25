import Foundation
import CoreGraphics

struct ClickArguments {
    let x: Double
    let y: Double
    let button: String
    let count: Int
    init(_ context: ActionContext) throws {
        try ActionArguments.keys(context, ["x", "y", "button", "count"])
        x = try ActionArguments.number(context.args["x"], name: "x")
        y = try ActionArguments.number(context.args["y"], name: "y")
        button = try ActionArguments.string(context.args["button"] ?? .string("left"), name: "button")
        guard ["left", "right"].contains(button) else { throw HelperError(.badRequest, "button must be left or right") }
        count = try ActionArguments.integer(context.args["count"] ?? .number(1), name: "count", range: 1...2)
    }
}

enum InputClickAction {
    static func make(probe: @escaping @Sendable () -> [String: JSONValue],
                     post: @escaping @Sendable ([CGEvent]) -> Void = { events in
                         for event in events { event.post(tap: .cghidEventTap) }
                     }) -> Action {
        Action { context in
            try ActionArguments.grant("accessibility", probe: probe)
            let args = try ClickArguments(context)
            let left = args.button == "left"
            let point = CGPoint(x: args.x, y: args.y)
            // Construct every event before posting any, so allocation errors never leave a button down.
            var events: [CGEvent] = []
            for click in 1...args.count {
                for type: CGEventType in [left ? .leftMouseDown : .rightMouseDown, left ? .leftMouseUp : .rightMouseUp] {
                    guard let event = CGEvent(mouseEventSource: nil, mouseType: type, mouseCursorPosition: point,
                                              mouseButton: left ? .left : .right) else { throw HelperError(.internalError, "cannot create mouse event") }
                    event.setIntegerValueField(.mouseEventClickState, value: Int64(click))
                    events.append(event)
                }
            }
            try context.checkDeadline()
            // Always finish the down/up sequence once started.
            post(events)
            return [:]
        }
    }
}
