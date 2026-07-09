// SCAFFOLD — NOT compiled or verified (no Xcode/Swift/Go-Apple toolchain on the
// authoring host). This is the packet-tunnel provider: the sandboxed extension
// process that links libbox and runs sing-box in-process. It is the hardest and
// most memory-constrained part of the port (the ~50 MB jetsam cap applies to this
// whole process — see docs/porting/ios.md#memory-budget).
//
// In SagerNet/sing-box-for-apple the equivalent NE target is a 4-line shim:
//     class PacketTunnelProvider: ExtensionProvider {}
// with all logic in a shared Library/Network/ExtensionProvider.swift (~332 lines).
// The scaffold inlines a reduced sketch of that logic here instead of vendoring
// their shared library. The libbox calls are guarded by #if canImport(Libbox)
// because the framework does not exist until scripts/build-libbox.sh runs on a Mac.

import NetworkExtension
import os

#if canImport(Libbox)
import Libbox
#endif

class PacketTunnelProvider: NEPacketTunnelProvider {
    private let log = Logger(subsystem: "com.tenebra.ios.tunnel", category: "tunnel")

    #if canImport(Libbox)
    // The libbox command server drives the engine lifecycle (start/stop/reload)
    // and forwards platform requests to our PlatformInterface. Held for the life
    // of the tunnel.
    private var commandServer: LibboxCommandServer?
    private var boxService: LibboxBoxService?
    private var platform: PlatformInterface?
    #endif

    // MARK: - Lifecycle

    override func startTunnel(options: [String: NSObject]?, completionHandler: @escaping (Error?) -> Void) {
        log.info("startTunnel")

        // Read the config the host app generated and wrote into the App Group
        // container (see TunnelManager.startConfiguredTunnel). The extension does
        // NOT generate configs — it only runs them — to keep allocation off the
        // memory-capped process.
        guard let configJSON = Self.readSharedConfig() else {
            completionHandler(PacketTunnelError.missingConfig)
            return
        }

        #if canImport(Libbox)
        do {
            try startEngine(configJSON: configJSON, completionHandler: completionHandler)
        } catch {
            log.error("startEngine failed: \(error.localizedDescription, privacy: .public)")
            completionHandler(error)
        }
        #else
        // No engine framework linked yet. Do not pretend to have a tunnel.
        completionHandler(PacketTunnelError.engineNotLinked)
        #endif
    }

    override func stopTunnel(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
        log.info("stopTunnel: \(String(describing: reason), privacy: .public)")
        #if canImport(Libbox)
        try? boxService?.close()
        boxService = nil
        commandServer?.close()
        commandServer = nil
        platform = nil
        #endif
        completionHandler()
    }

    // app -> extension messages. With the libbox CommandServer/CommandClient in
    // place, most control flows through that IPC instead; handleAppMessage stays
    // available for simple request/response probes. TODO: define the message set.
    override func handleAppMessage(_ messageData: Data, completionHandler: ((Data?) -> Void)?) {
        completionHandler?(nil)
    }

    // The system suspends and resumes the extension around device sleep. libbox
    // exposes matching hooks so the engine can pause timers/connections and cut
    // idle memory — important under the jetsam cap.
    override func sleep(completionHandler: @escaping () -> Void) {
        #if canImport(Libbox)
        boxService?.pause()
        #endif
        completionHandler()
    }

    override func wake() {
        #if canImport(Libbox)
        boxService?.wake()
        #endif
    }

    // MARK: - Engine

    #if canImport(Libbox)
    // Bring up sing-box in-process. This sketches the libbox 1.13 surface; the
    // exact initializers/among LibboxSetup, LibboxNewCommandServer,
    // StartOrReloadService come from the generated Libbox headers — confirm them
    // on the Mac. The load-bearing steps are: (1) LibboxSetup once with base/temp
    // paths in the App Group and a memory limit WELL below 50 MB; (2) install our
    // PlatformInterface so libbox can call back for the utun fd et al.; (3) hand
    // the config JSON to StartOrReloadService, which internally builds the box.
    private func startEngine(configJSON: String, completionHandler: @escaping (Error?) -> Void) throws {
        let base = Self.appGroupBasePath()

        // 1. One-time setup. The memory limit + OOM-killer knob is the primary
        //    lever against the 50 MB cap; pair with aggressive GC in the Go core.
        var setupError: NSError?
        let options = LibboxSetupOptions()
        options.basePath = base
        options.workingPath = base + "/work"
        options.tempPath = base + "/temp"
        // options.isTVOS = false
        LibboxSetup(options, &setupError)
        if let setupError { throw setupError }

        // 2. PlatformInterface — libbox calls back into this for the utun fd (from
        //    self.packetFlow), interface monitoring, Wi-Fi state, process lookup.
        let platform = PlatformInterface(provider: self)
        self.platform = platform

        // 3. CommandServer + StartOrReloadService(config). The server also backs
        //    the app's CommandClient (status/logs/SelectOutbound) over the App
        //    Group unix socket.
        let server = LibboxNewCommandServer(platform, /* maxLines */ 100)
        try server?.start()
        self.commandServer = server

        // NOTE: in libbox 1.11+ there is no standalone NewService; the engine is
        // booted via CommandServer.StartOrReloadService(configJSON). Verify the
        // exact call and how the resulting service handle is retrieved.
        // try server?.startService(withConfig: configJSON)   // <- confirm name

        completionHandler(nil)
    }
    #endif

    // MARK: - App Group I/O

    private static func appGroupBasePath() -> String {
        FileManager.default
            .containerURL(forSecurityApplicationGroupIdentifier: "group.com.tenebra.ios")?
            .path ?? NSTemporaryDirectory()
    }

    private static func readSharedConfig() -> String? {
        guard let url = FileManager.default
            .containerURL(forSecurityApplicationGroupIdentifier: "group.com.tenebra.ios")?
            .appendingPathComponent("current-config.json") else { return nil }
        return try? String(contentsOf: url, encoding: .utf8)
    }
}

enum PacketTunnelError: LocalizedError {
    case missingConfig
    case engineNotLinked

    var errorDescription: String? {
        switch self {
        case .missingConfig: return "No sing-box config found in the App Group container"
        case .engineNotLinked: return "Libbox.xcframework is not linked — run scripts/build-libbox.sh on a Mac"
        }
    }
}
