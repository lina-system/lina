import Foundation
import ScreenCaptureKit
import CoreGraphics
import ImageIO
import UniformTypeIdentifiers

struct ScreenshotArguments {
    enum Target: Equatable, Sendable { case main, display(UInt32), window(UInt32) }
    let target: Target
    init(_ context: ActionContext) throws {
        try ActionArguments.keys(context, ["display", "window"])
        guard context.args["display"] == nil || context.args["window"] == nil else {
            throw HelperError(.badRequest, "choose display or window")
        }
        if let window = context.args["window"] {
            target = .window(UInt32(try ActionArguments.integer(window, name: "window", range: 1...Int(UInt32.max))))
        } else if let display = context.args["display"], display != .string("main") {
            target = .display(UInt32(try ActionArguments.integer(display, name: "display", range: 1...Int(UInt32.max))))
        } else { target = .main }
    }
}

enum ScreenshotAction {
    static func make(outbox: Outbox, probe: @escaping @Sendable () -> [String: JSONValue]) -> Action {
        Action(timeout: 15) { context in
            try ActionArguments.grant("screen_recording", probe: probe)
            let args = try ScreenshotArguments(context)
            try outbox.cleanup()
            guard #available(macOS 14.0, *) else { throw HelperError(.internalError, "screenshots require macOS 14 or later") }
            let completion = ActionCompletion<CapturedPNG>()
            SCShareableContent.getExcludingDesktopWindows(false, onScreenWindowsOnly: false) { content, error in
                do {
                    try context.checkDeadline()
                    guard let content, error == nil else { throw captureError(probe) }
                    let filter: SCContentFilter
                    let width: Int, height: Int
                    switch args.target {
                    case .main, .display:
                        let id: CGDirectDisplayID
                        if case .display(let selected) = args.target { id = selected } else { id = CGMainDisplayID() }
                        guard let display = content.displays.first(where: { $0.displayID == id }) else {
                            throw HelperError(.targetNotFound, "display not found")
                        }
                        filter = SCContentFilter(display: display, excludingWindows: [])
                        width = CGDisplayPixelsWide(id); height = CGDisplayPixelsHigh(id)
                    case .window(let id):
                        guard let window = content.windows.first(where: { $0.windowID == id }) else {
                            throw HelperError(.targetNotFound, "window not found")
                        }
                        filter = SCContentFilter(desktopIndependentWindow: window)
                        width = Int(ceil(filter.contentRect.width * CGFloat(filter.pointPixelScale)))
                        height = Int(ceil(filter.contentRect.height * CGFloat(filter.pointPixelScale)))
                    }
                    guard width > 0, height > 0 else { throw HelperError(.targetNotFound, "empty capture target") }
                    let configuration = SCStreamConfiguration()
                    configuration.width = width; configuration.height = height
                    configuration.showsCursor = false
                    try context.checkDeadline()
                    SCScreenshotManager.captureImage(contentFilter: filter, configuration: configuration) { image, error in
                        do {
                            try context.checkDeadline()
                            guard let image, error == nil else { throw captureError(probe) }
                            let data = NSMutableData()
                            guard let destination = CGImageDestinationCreateWithData(data, UTType.png.identifier as CFString, 1, nil) else {
                                throw HelperError(.internalError, "cannot encode PNG")
                            }
                            CGImageDestinationAddImage(destination, image, nil)
                            guard CGImageDestinationFinalize(destination) else { throw HelperError(.internalError, "cannot encode PNG") }
                            completion.finish(.success(CapturedPNG(data: data as Data, width: image.width, height: image.height)))
                        } catch { completion.finish(.failure((error as? HelperError) ?? HelperError(.internalError, "capture failed"))) }
                    }
                } catch { completion.finish(.failure((error as? HelperError) ?? HelperError(.internalError, "capture failed"))) }
            }
            let image = try completion.wait(context)
            try context.checkDeadline()
            let path = try outbox.writePNG(image.data)
            return ["path": .string(path), "width": .number(Double(image.width)), "height": .number(Double(image.height))]
        }
    }
    private static func captureError(_ probe: @Sendable () -> [String: JSONValue]) -> HelperError {
        probe()["screen_recording"] == .bool(true) ? HelperError(.internalError, "screen capture failed") : HelperError(.permissionMissing, "screen_recording")
    }
    private struct CapturedPNG: Sendable { let data: Data; let width: Int; let height: Int }
}
