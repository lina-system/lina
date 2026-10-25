import Foundation
import CoreGraphics

struct KeysArguments {
    static let keyCodes: [String: CGKeyCode] = [
        "return": 36, "tab": 48, "escape": 53, "space": 49, "delete": 51,
        "up": 126, "down": 125, "left": 123, "right": 124, "home": 115, "end": 119,
        "pageup": 116, "pagedown": 121, "f1": 122, "f2": 120, "f3": 99, "f4": 118,
        "f5": 96, "f6": 97, "f7": 98, "f8": 100, "f9": 101, "f10": 109, "f11": 103, "f12": 111,
    ]
    let text: String?
    let keyCode: CGKeyCode?
    let flags: CGEventFlags
    init(_ context: ActionContext) throws {
        try ActionArguments.keys(context, ["text", "key", "modifiers"])
        guard (context.args["text"] != nil) != (context.args["key"] != nil) else { throw HelperError(.badRequest, "choose text or key") }
        if let value = context.args["text"] {
            guard let string = value.string, string.count <= 512, context.args["modifiers"] == nil else {
                throw HelperError(.badRequest, "text must be at most 512 characters without modifiers")
            }
            text = string; keyCode = nil; flags = []
        } else {
            let key = try ActionArguments.string(context.args["key"], name: "key")
            guard let code = Self.keyCodes[key] else { throw HelperError(.badRequest, "unsupported named key") }
            keyCode = code; text = nil
            let allowed: [String: CGEventFlags] = ["cmd": .maskCommand, "shift": .maskShift, "option": .maskAlternate, "ctrl": .maskControl]
            guard case .array(let modifiers) = context.args["modifiers"] ?? .array([]) else { throw HelperError(.badRequest, "modifiers must be an array") }
            var seen: Set<String> = [], collected: CGEventFlags = []
            for modifier in modifiers {
                guard let name = modifier.string, let flag = allowed[name], seen.insert(name).inserted else {
                    throw HelperError(.badRequest, "invalid or duplicate modifier")
                }
                collected.insert(flag)
            }
            flags = collected
        }
    }
}

enum InputKeysAction {
    static func make(probe: @escaping @Sendable () -> [String: JSONValue],
                     post: @escaping @Sendable ([CGEvent]) -> Void = { events in
                         for event in events { event.post(tap: .cghidEventTap) }
                     }) -> Action {
        Action { context in
            try ActionArguments.grant("accessibility", probe: probe)
            let args = try KeysArguments(context)
            if args.text == "" { return [:] }
            guard let down = CGEvent(keyboardEventSource: nil, virtualKey: args.keyCode ?? 0, keyDown: true),
                  let up = CGEvent(keyboardEventSource: nil, virtualKey: args.keyCode ?? 0, keyDown: false) else {
                throw HelperError(.internalError, "cannot create keyboard event")
            }
            for event in [down, up] {
                event.flags = args.flags
                if let text = args.text {
                    let units = Array(text.utf16)
                    units.withUnsafeBufferPointer { buffer in
                        event.keyboardSetUnicodeString(stringLength: buffer.count, unicodeString: buffer.baseAddress)
                    }
                }
            }
            try context.checkDeadline()
            post([down, up])
            return [:]
        }
    }
}
