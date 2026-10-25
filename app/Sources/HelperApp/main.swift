import HelperCore
import AppKit
import OSLog

final class LifecycleDelegate: NSObject, NSApplicationDelegate {
    let stop: () -> Void
    init(stop: @escaping () -> Void) { self.stop = stop }
    func applicationWillTerminate(_ notification: Notification) { stop() }
}

let app = NSApplication.shared
app.setActivationPolicy(.accessory)
let configuration = Configuration()
do {
    let audit = try AuditLog(directory: configuration.auditDirectory)
    let outbox = Outbox(home: configuration.home)
    let cua = CuaDriverSupervisor(home: configuration.home, policyPath: configuration.policyPath, audit: audit)
    defer { cua.shutdown() }
    let dispatcher = ActionDispatcher(actions: SystemActions.registry(home: configuration.home, outbox: outbox, cua: cua))
    let server = HelperServer(configuration: configuration, dispatcher: dispatcher, audit: audit, outbox: outbox, cua: cua)
    try server.start()
    let delegate = LifecycleDelegate { server.stop(); cua.shutdown() }
    app.delegate = delegate
    signal(SIGTERM, SIG_IGN)
    signal(SIGINT, SIG_IGN)
    let signals = [SIGTERM, SIGINT].map { number in
        let source = DispatchSource.makeSignalSource(signal: number, queue: .main)
        source.setEventHandler { server.stop(); cua.shutdown(); app.terminate(nil) }
        source.resume()
        return source
    }
    cua.startMonitoring()
    withExtendedLifetime((server, signals, cua, delegate)) { app.run() }
    server.stop()
} catch {
    Logger(subsystem: "app.lina-os.helper", category: "startup").error("helper startup failed")
    let detail = (error as? HelperError)?.detail ?? "startup failed"
    try? FileHandle.standardError.write(contentsOf: Data((detail + "\n").utf8))
    exit(1)
}
