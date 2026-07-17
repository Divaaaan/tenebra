// Scaffold: not compiled on the authoring host (no Android SDK / libbox.aar).
// Mirrors io.nekohasekai.sfa.bg.PlatformInterfaceWrapper from
// SagerNet/sing-box-for-android (GPL-3.0). Tenebra is GPL-3.0-compatible.
// Upstream: https://github.com/SagerNet/sing-box-for-android
//
// This targets the MODERN libbox surface (sing-box ~1.13, matching the pinned engine
// tag). The binding has drifted across sing-box versions; every point that changed is
// flagged "verify against generated libbox.aar". The single reliable tell after the
// first bind: `findConnectionOwner` returning ConnectionOwner and `systemCertificates`
// existing => modern (this file); returning Int with packageNameByUid/uidByPackageName
// => classic (adjust the flagged members).
package com.tenebra.android.bg

import android.os.Build
import io.nekohasekai.libbox.ConnectionOwner
import io.nekohasekai.libbox.InterfaceUpdateListener
import io.nekohasekai.libbox.LocalDNSTransport
import io.nekohasekai.libbox.NetworkInterface
import io.nekohasekai.libbox.NetworkInterfaceIterator
import io.nekohasekai.libbox.PlatformInterface
import io.nekohasekai.libbox.StringIterator
import io.nekohasekai.libbox.TunOptions
import io.nekohasekai.libbox.WIFIState
import java.net.NetworkInterface as JavaNetworkInterface

// Default implementations of libbox's PlatformInterface. The concrete VpnService
// implements this interface and overrides only the members that need the service
// itself: openTun, autoDetectInterfaceControl (-> protect), and sendNotification.
// Everything else is a sensible default here.
interface PlatformWrapper : PlatformInterface {

    // The engine controls its own upstream socket protection through this pair rather
    // than the platform enumerating/binding interfaces: return true, and libbox hands
    // each freshly-created socket fd to autoDetectInterfaceControl, where the service
    // calls VpnService.protect(fd) so that socket bypasses the tun. This is the core
    // of not looping the tunnel's own traffic back into itself.
    override fun usePlatformAutoDetectInterfaceControl(): Boolean = true

    override fun autoDetectInterfaceControl(fd: Int) {
        // VpnService overrides this to call protect(fd).
    }

    override fun openTun(options: TunOptions): Int =
        error("openTun must be implemented by the VpnService")

    // /proc/net was closed to apps in Android 10; below that libbox can read it to
    // resolve connection ownership itself, so the platform lookup isn't needed.
    override fun useProcFS(): Boolean = Build.VERSION.SDK_INT < Build.VERSION_CODES.Q

    // Only used by owner-matching routing rules, which this minimal client does not
    // emit. Returns an empty owner so the engine treats ownership as unknown.
    // verify against generated libbox.aar: modern libbox returns ConnectionOwner;
    // classic returns Int (the uid) and additionally declares packageNameByUid /
    // uidByPackageName. Adjust here if the bind reports the classic shape.
    override fun findConnectionOwner(
        ipProtocol: Int,
        sourceAddress: String,
        sourcePort: Int,
        destinationAddress: String,
        destinationPort: Int,
    ): ConnectionOwner = ConnectionOwner()

    override fun startDefaultInterfaceMonitor(listener: InterfaceUpdateListener) {
        DefaultNetworkMonitor.register(listener)
    }

    override fun closeDefaultInterfaceMonitor(listener: InterfaceUpdateListener) {
        DefaultNetworkMonitor.unregister(listener)
    }

    // Interface enumeration for the engine. Built from java.net so it needs no
    // Context. Type/metered are coarse (Other/false) — enough for socket binding;
    // enriching them per-network via ConnectivityManager is a later refinement.
    // verify against generated libbox.aar: NetworkInterface field names/setters below.
    override fun getInterfaces(): NetworkInterfaceIterator {
        val out = ArrayList<NetworkInterface>()
        val ifaces = runCatching { JavaNetworkInterface.getNetworkInterfaces() }.getOrNull()
        if (ifaces != null) {
            for (ni in ifaces) {
                if (!runCatching { ni.isUp }.getOrDefault(false)) continue
                val addressStrings = ni.interfaceAddresses.mapNotNull { addr ->
                    addr.address?.hostAddress?.let { "$it/${addr.networkPrefixLength}" }
                }
                out += NetworkInterface().apply {
                    name = ni.name
                    index = runCatching { android.system.Os.if_nametoindex(ni.name) }.getOrDefault(0)
                    mtu = runCatching { ni.mtu }.getOrDefault(1500)
                    addresses = StringArray(addressStrings.iterator())
                    flags = interfaceFlags(ni)
                }
            }
        }
        return InterfaceArray(out.iterator())
    }

    // Compute the net.Flags bitmask libbox expects (up=1, broadcast=2, loopback=4,
    // pointToPoint=8, multicast=16). verify the exact bit meaning against libbox.
    private fun interfaceFlags(ni: JavaNetworkInterface): Int {
        var flags = 0
        runCatching {
            if (ni.isUp) flags = flags or 1
            if (ni.supportsMulticast()) flags = flags or 16
            if (ni.isLoopback) flags = flags or 4
            if (ni.isPointToPoint) flags = flags or 8
        }
        return flags
    }

    override fun underNetworkExtension(): Boolean = false

    override fun includeAllNetworks(): Boolean = false

    override fun clearDNSCache() {}

    // SSID/BSSID needs the location permission on modern Android; the minimal client
    // does not request it and does no Wi-Fi-conditional routing, so report none.
    override fun readWIFIState(): WIFIState? = null

    // Let sing-box resolve DNS itself rather than proxying it through an Android
    // LocalResolver. verify: modern libbox has localDNSTransport(); classic does not.
    override fun localDNSTransport(): LocalDNSTransport? = null

    // Use the certificates bundled with the engine. verify: modern libbox has
    // systemCertificates(); classic does not.
    override fun systemCertificates(): StringIterator = StringArray(emptyList<String>().iterator())

    // libbox -> platform notifications (engine warnings). The VpnService overrides
    // this to surface them; default is a no-op so a non-service implementor is inert.
    // Signature intentionally omitted here because the libbox Notification type name
    // differs across versions — declared and overridden in the service. verify.

    // --- gomobile iterator adapters (nested, exactly as SFA structures them) ---

    class StringArray(private val iterator: Iterator<String>) : StringIterator {
        override fun hasNext(): Boolean = iterator.hasNext()
        override fun next(): String = iterator.next()
        // Present on modern libbox; the core does not rely on it.
        override fun len(): Int = 0
    }

    class InterfaceArray(private val iterator: Iterator<NetworkInterface>) : NetworkInterfaceIterator {
        override fun hasNext(): Boolean = iterator.hasNext()
        override fun next(): NetworkInterface = iterator.next()
    }
}
