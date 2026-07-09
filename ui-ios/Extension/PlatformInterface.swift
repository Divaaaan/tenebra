// SCAFFOLD — NOT compiled or verified (no Xcode/Swift/Go-Apple toolchain on the
// authoring host). PlatformInterface is the Go<->Swift bridge sing-box calls into
// for the platform-owned pieces the Network Extension controls: the utun file
// descriptor, default-interface monitoring, Wi-Fi state, and process lookup for
// connection ownership. libbox (Go) invokes these; we implement them in Swift.
//
// The reference implementation is sing-box-for-apple's
// Library/Network/ExtensionPlatformInterface.swift (~879 lines). The protocol
// name and its exact method set are defined by the GENERATED Libbox headers and
// vary by sing-box version — the methods below are the load-bearing ones sketched
// from the 1.13 libbox surface, NOT a verified conformance. Read the real headers
// on the Mac and complete the conformance there. The whole file is guarded on the
// framework being present.

import Foundation
import NetworkExtension

#if canImport(Libbox)
import Libbox

// The concrete protocol is `LibboxPlatformInterfaceProtocol` (gomobile names it
// from the Go `PlatformInterface` interface). Declaring conformance is deferred to
// the Mac because the member set must match the generated header exactly; missing
// or misspelled members are a compile error there, which is the right place to
// resolve them.
final class PlatformInterface: NSObject /*, LibboxPlatformInterfaceProtocol */ {
    // Weak back-reference to the provider that owns the NEPacketTunnelFlow — the
    // source of the utun file descriptor libbox needs.
    private weak var provider: NEPacketTunnelProvider?

    init(provider: NEPacketTunnelProvider) {
        self.provider = provider
        super.init()
    }

    // openTun — the crux of the whole bridge. libbox asks for the tunnel file
    // descriptor; on iOS the OS has already granted it to the entitled extension
    // via NEPacketTunnelFlow, so no root is ever needed. The fd is fetched from the
    // provider's packetFlow (historically via a KVC hop to `socket.fileDescriptor`
    // since Apple exposes no public API for it) after setTunnelNetworkSettings has
    // applied the addresses/routes libbox passes in `options`.
    //
    // Signature is illustrative; match it to the generated header on the Mac.
    // func openTun(_ options: LibboxTunOptions?) throws -> Int32 { ... }

    // autoDetectInterfaceControl / interface monitoring — libbox needs to know the
    // current default (physical) interface to route the tunnel's own upstream
    // traffic and to react to network changes (Wi-Fi <-> cellular). Back these with
    // an nw_path_monitor.
    // func usePlatformDefaultInterfaceMonitor() -> Bool { true }
    // func startDefaultInterfaceMonitor(_ listener: ...) throws { ... }
    // func closeDefaultInterfaceMonitor(_ listener: ...) throws { ... }

    // Wi-Fi state (SSID/BSSID) for on-demand and diagnostics. Requires the
    // location entitlement on modern iOS to read the SSID; return empty otherwise.
    // func readWIFIState() -> LibboxWIFIState? { nil }

    // Process/connection ownership lookup — used by rules that match on the owning
    // app. Not available to a sandboxed iOS NE the way it is on macOS; return
    // "not found" / disabled.
    // func useProcFS() -> Bool { false }
    // func findConnectionOwner(...) throws -> ... { ... }

    // Logging + local rule-set/asset paths, if the header requires them, resolve
    // into the App Group container. The extension must READ cached .srs rule-sets
    // the host app downloaded — never fetch them here, per the memory budget.

    // TODO (on Mac): declare `: LibboxPlatformInterfaceProtocol` and implement the
    // full member set the generated header lists; delete this note once it builds.
}

#endif
