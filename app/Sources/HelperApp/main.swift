import HelperCore
import AppKit
import OSLog

let app = NSApplication.shared
app.setActivationPolicy(.accessory)
let configuration = Configuration()
do {
    let audit = try AuditLog(directory: configuration.auditDirectory)
    let dispatcher = ActionDispatcher(actions: SystemActions.registry(home: configuration.home))
    let server = HelperServer(configuration: configuration, dispatcher: dispatcher, audit: audit)
    try server.start()
    signal(SIGTERM, SIG_IGN)
    signal(SIGINT, SIG_IGN)
    let signals = [SIGTERM, SIGINT].map { number in
        let source = DispatchSource.makeSignalSource(signal: number, queue: .main)
        source.setEventHandler { server.stop(); app.terminate(nil) }
        source.resume()
        return source
    }
    withExtendedLifetime((server, signals)) { app.run() }
    server.stop()
} catch {
    Logger(subsystem: "app.lina-os.helper", category: "startup").error("helper startup failed")
    let detail = (error as? HelperError)?.detail ?? "startup failed"
    try? FileHandle.standardError.write(contentsOf: Data((detail + "\n").utf8))
    exit(1)
}
