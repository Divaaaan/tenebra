// SCAFFOLD — NOT compiled or verified. This file was written on a Windows host
// with no Xcode/Swift toolchain; it has never been through swiftc. It follows the
// SwiftUI App-lifecycle pattern and the sing-box-for-apple (SFI) structure as a
// starting point, but treat it as a reviewed sketch, not working code. Build and
// fix it on macOS + Xcode (see ui-ios/README.md).

import SwiftUI

// TenebraApp is the SwiftUI entry point for the host app. The app is deliberately
// thin: it owns a TunnelManager (which provisions and observes the Network
// Extension) and draws the node list + connect toggle over it. It never sees a
// packet — the tunnel runs entirely in the extension process.
@main
struct TenebraApp: App {
    // One TunnelManager for the app's lifetime, shared into the view tree.
    @StateObject private var tunnel = TunnelManager()

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(tunnel)
                // Load (or lazily create) the saved NETunnelProviderManager on
                // launch so the connect toggle reflects the real VPN state.
                .task { await tunnel.loadTunnelPreferences() }
                // tenebra:// deep link — import a subscription from an external
                // source (e.g. a "Open in Tenebra" button). Neutral scheme; see
                // Support/App/Info.plist.
                .onOpenURL { url in tunnel.handleImportURL(url) }
        }
    }
}
