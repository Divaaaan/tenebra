// SCAFFOLD — NOT compiled or verified (no Xcode/Swift on the authoring host).
// This is the app-side tunnel controller: it provisions the Network Extension via
// NETunnelProviderManager, observes NEVPNStatus, generates configs through the
// TenebraCore gomobile framework, and (once wired) drives the extension over
// libbox's CommandClient. It follows Mullvad's TunnelManager and SFI's
// ExtensionProfile as references. Every method here needs to be built and tested
// on macOS + Xcode against a real device — the NE provisioning and IPC paths in
// particular have well-known gotchas called out inline. See ui-ios/README.md.

import Foundation
import NetworkExtension
import Combine

// The gomobile frameworks only exist after scripts/build-libbox.sh runs on a Mac.
// Guard the imports so the rest of this file is at least syntactically reviewable
// without them, and so the "no framework yet" state is explicit rather than a
// hard compile break the reader has to infer.
#if canImport(TenebraCore)
import TenebraCore
#endif
#if canImport(Libbox)
import Libbox
#endif

// Shared identifiers. Keep in sync with project.yml and the entitlements.
enum TunnelConstants {
    static let extensionBundleID = "com.tenebra.ios.tunnel"
    static let appGroup = "group.com.tenebra.ios"
    // A neutral display name for the saved VPN configuration (shown in
    // Settings > VPN). No brand/infrastructure leakage.
    static let displayName = "Tenebra"
}

// A minimal node view-model. The real type mirrors the core's model.Node /
// profile.Server (see core/model/model.go); this carries only what the list needs.
struct NodeStub: Identifiable, Equatable {
    let id: String      // stable server id (profile.Server.ID)
    let name: String
}

@MainActor
final class TunnelManager: ObservableObject {
    @Published private(set) var status: NEVPNStatus = .invalid
    @Published private(set) var nodes: [NodeStub] = []
    @Published var selectedNode: NodeStub?

    // The single saved provider manager. nil until loadTunnelPreferences runs.
    private var manager: NETunnelProviderManager?
    private var statusObserver: NSObjectProtocol?

    var isActive: Bool { status == .connected || status == .connecting || status == .reasserting }

    var statusText: String {
        switch status {
        case .invalid: return "Not configured"
        case .disconnected: return "Disconnected"
        case .connecting: return "Connecting…"
        case .connected: return "Connected"
        case .reasserting: return "Reconnecting…"
        case .disconnecting: return "Disconnecting…"
        @unknown default: return "Unknown"
        }
    }

    // MARK: - Provisioning

    // Load the saved NETunnelProviderManager, if any. The app can hold at most one
    // packet-tunnel configuration it created; loadAllFromPreferences returns it (or
    // an empty array on first run).
    func loadTunnelPreferences() async {
        do {
            let managers = try await NETunnelProviderManager.loadAllFromPreferences()
            manager = managers.first
            observeStatus()
            status = manager?.connection.status ?? .invalid
        } catch {
            // TODO: surface to the UI. Left as a log for the scaffold.
            NSLog("Tenebra: loadAllFromPreferences failed: \(error)")
        }
    }

    // Create or update the saved VPN configuration. The first saveToPreferences
    // triggers the system authorization prompt (Face/Touch ID or passcode).
    //
    // GOTCHA (well-documented): you often must loadFromPreferences() again
    // immediately after saveToPreferences() before the config is usable, or the
    // next startVPNTunnel throws. Mirrored here as a reload step.
    func saveTunnelPreferences() async throws {
        let mgr = manager ?? NETunnelProviderManager()

        let proto = NETunnelProviderProtocol()
        proto.providerBundleIdentifier = TunnelConstants.extensionBundleID
        // serverAddress is a required, user-visible label, not a real address for a
        // multi-node client. A neutral placeholder avoids leaking any endpoint.
        proto.serverAddress = "Tenebra"
        // Small key/values only. The full sing-box config is NOT passed here — it
        // is generated on connect and handed to the extension over the CommandServer
        // IPC (see startConfiguredTunnel). providerConfiguration has a tight size
        // budget and would bloat the system VPN DB.
        proto.providerConfiguration = [:]

        mgr.protocolConfiguration = proto
        mgr.localizedDescription = TunnelConstants.displayName
        mgr.isEnabled = true
        // On-demand (auto-connect on untrusted Wi-Fi) is configured here in a later
        // stage; left off for the scaffold. See docs/porting/ios.md (I4).
        mgr.isOnDemandEnabled = false

        try await mgr.saveToPreferences()
        try await mgr.loadFromPreferences() // reload-after-save gotcha
        manager = mgr
        observeStatus()
    }

    // MARK: - Connect / disconnect

    func connect() async {
        do {
            if manager == nil { try await saveTunnelPreferences() }
            try await startConfiguredTunnel()
        } catch {
            NSLog("Tenebra: connect failed: \(error)")
        }
    }

    func disconnect() async {
        manager?.connection.stopVPNTunnel()
    }

    // Generate the sing-box config for the selected node and start the tunnel.
    //
    // The config is produced by Tenebra's core (GenerateConfig) — NOT inside the
    // extension — because the generator is a small, allocation-light call and the
    // extension lives under the 50 MB jetsam cap. The JSON then reaches the engine
    // one of two ways, to be decided when the IPC lands (see docs/porting/ios.md):
    //   (a) write it into the App Group container and start the tunnel with a
    //       pointer to it in the startTunnel options, or
    //   (b) start the tunnel, then push it via libbox CommandClient
    //       -> CommandServer.StartOrReloadService(json).
    // The scaffold sketches (a) since it needs no live IPC to bring a tunnel up.
    private func startConfiguredTunnel() async throws {
        guard let manager else { return }

        let configJSON = try generateConfigJSON()

        // Persist the config into the shared App Group container for the extension
        // to read on startTunnel. TODO: define the on-disk contract (path, atomic
        // write) and the matching read in PacketTunnelProvider.
        if let url = Self.appGroupConfigURL() {
            try configJSON.data(using: .utf8)?.write(to: url, options: .atomic)
        }

        // options are passed to the provider's startTunnel(options:). Keep them
        // tiny — a filename/flag, never the whole config.
        try manager.connection.startVPNTunnel(options: [:])
    }

    // Call into Tenebra's core to turn the current profile + selection into a
    // sing-box config JSON string. This is the shared gomobile bridge in core-bridge.
    private func generateConfigJSON() throws -> String {
        // Build the request envelope the bridge expects (see bridge.go:generateRequest).
        // TODO: assemble a real profile JSON from stored state; placeholder here.
        let requestJSON = "{\"profile\":{\"id\":\"\",\"name\":\"\",\"source\":\"manual\",\"nodes\":[]}}"

        #if canImport(TenebraCore)
        // gomobile surfaces the Go function roughly as below; confirm the exact
        // symbol/signature against the generated TenebraCore headers on the Mac
        // (gomobile prefixes with the capitalized package name and returns via an
        // NSError out-param that Swift imports as `throws`).
        return try TenebracoreGenerateConfig(requestJSON)
        #else
        // No framework built yet. Fail loudly rather than pretend.
        throw NSError(domain: "Tenebra", code: -1, userInfo: [
            NSLocalizedDescriptionKey: "TenebraCore.xcframework not built — run scripts/build-libbox.sh on a Mac"
        ])
        #endif
    }

    // MARK: - Node selection & import

    func select(_ node: NodeStub) {
        selectedNode = node
        // When connected, a node switch is a libbox CommandClient.SelectOutbound
        // call rather than a full reconnect. TODO once the IPC lands.
    }

    // Handle a tenebra:// deep link (subscription import). TODO: parse and store.
    func handleImportURL(_ url: URL) {
        NSLog("Tenebra: import URL received: \(url) (parsing TODO)")
    }

    // MARK: - Status observation

    private func observeStatus() {
        guard let connection = manager?.connection else { return }
        if let statusObserver { NotificationCenter.default.removeObserver(statusObserver) }
        statusObserver = NotificationCenter.default.addObserver(
            forName: .NEVPNStatusDidChange,
            object: connection,
            queue: .main
        ) { [weak self] _ in
            Task { @MainActor in self?.status = connection.status }
        }
    }

    // MARK: - App Group helpers

    private static func appGroupConfigURL() -> URL? {
        FileManager.default
            .containerURL(forSecurityApplicationGroupIdentifier: TunnelConstants.appGroup)?
            .appendingPathComponent("current-config.json")
    }

    deinit {
        if let statusObserver { NotificationCenter.default.removeObserver(statusObserver) }
    }
}
