// Scaffold: not compiled on the authoring host (no Android SDK). Mirrors the
// default-network tracking in SagerNet/sing-box-for-android (io.nekohasekai.sfa.bg,
// GPL-3.0). Upstream: https://github.com/SagerNet/sing-box-for-android
package com.tenebra.android.bg

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.system.Os
import io.nekohasekai.libbox.InterfaceUpdateListener

// Tracks the system default (underlying) network so libbox can route the tunnel's
// OWN upstream sockets over the real interface and react to Wi-Fi<->cellular
// handovers. Backs PlatformWrapper.start/closeDefaultInterfaceMonitor. Started once
// by the service with an application Context, so the PlatformInterface default
// methods themselves stay Context-free.
object DefaultNetworkMonitor {

    private var connectivity: ConnectivityManager? = null
    private var callback: ConnectivityManager.NetworkCallback? = null

    // Current default interface, pushed to every registered libbox listener on change
    // and to any listener the moment it registers.
    @Volatile private var defaultInterfaceName: String = ""
    @Volatile private var defaultInterfaceIndex: Int = -1

    private val listeners = mutableSetOf<InterfaceUpdateListener>()

    @Synchronized
    fun start(context: Context) {
        if (callback != null) return
        val cm = context.applicationContext
            .getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
        connectivity = cm
        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onLinkPropertiesChanged(network: Network, props: LinkProperties) {
                // Once our own tunnel is up, the app's default network becomes the VPN
                // itself, so this callback reports tun0 as the default. Binding the
                // engine's upstream sockets there loops the tunnel into itself and
                // sing-box fails every dial with "no available network interface".
                // Skip any VPN network and keep the last real underlying interface.
                if (isVpn(cm, network)) return
                updateFrom(props)
            }

            override fun onLost(network: Network) {
                // Leave the last-known interface in place; sing-box handles the drop
                // and a new default arrives via onLinkPropertiesChanged.
            }
        }
        // registerDefaultNetworkCallback tracks the interface that actually carries
        // default traffic — exactly what the engine must bind its upstream to.
        cm.registerDefaultNetworkCallback(cb)
        callback = cb
    }

    @Synchronized
    fun stop() {
        val cm = connectivity ?: return
        callback?.let { runCatching { cm.unregisterNetworkCallback(it) } }
        callback = null
        connectivity = null
        listeners.clear()
        defaultInterfaceName = ""
        defaultInterfaceIndex = -1
    }

    @Synchronized
    fun register(listener: InterfaceUpdateListener) {
        listeners += listener
        if (defaultInterfaceName.isNotEmpty()) {
            pushTo(listener)
        }
    }

    @Synchronized
    fun unregister(listener: InterfaceUpdateListener) {
        listeners -= listener
    }

    // A network with TRANSPORT_VPN (equivalently, lacking NET_CAPABILITY_NOT_VPN) is
    // our own tunnel. Treat an unknown/null capability set as non-VPN so a transient
    // lookup miss never drops a real underlying interface.
    private fun isVpn(cm: ConnectivityManager, network: Network): Boolean {
        val caps = runCatching { cm.getNetworkCapabilities(network) }.getOrNull() ?: return false
        return caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN) ||
            !caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
    }

    @Synchronized
    private fun updateFrom(props: LinkProperties) {
        val name = props.interfaceName ?: return
        val index = runCatching { Os.if_nametoindex(name) }.getOrDefault(0)
        if (name == defaultInterfaceName && index == defaultInterfaceIndex) return
        defaultInterfaceName = name
        defaultInterfaceIndex = index
        listeners.forEach { pushTo(it) }
    }

    private fun pushTo(listener: InterfaceUpdateListener) {
        // verify against generated tenebra.aar: modern libbox signature is
        // updateDefaultInterface(interfaceName, interfaceIndex, isExpensive, isConstrained).
        runCatching {
            listener.updateDefaultInterface(
                defaultInterfaceName,
                defaultInterfaceIndex,
                false,
                false,
            )
        }
    }
}
