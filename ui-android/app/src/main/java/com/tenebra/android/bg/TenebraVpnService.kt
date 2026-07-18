// Scaffold: not compiled on the authoring host (no Android SDK / tenebra.aar).
// Mirrors io.nekohasekai.sfa.bg.VPNService from SagerNet/sing-box-for-android
// (GPL-3.0). Tenebra is GPL-3.0-compatible.
// Upstream: https://github.com/SagerNet/sing-box-for-android
//
// Divergence from SFA that matters: our core generates the tun with externalTun set,
// so the config omits auto_route and libbox reports options.autoRoute == false. On
// Android WE own routing, so openTun adds the default routes to the VpnService.Builder
// unconditionally rather than gating on options.autoRoute the way SFA does. sing-box
// still splits LAN/direct traffic via its own route rules, and its direct sockets are
// protect()'d, so nothing loops back into the tun.
package com.tenebra.android.bg

import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import android.util.Log
import androidx.core.content.ContextCompat
import com.tenebra.android.core.ConfigGenerator
import com.tenebra.android.store.ProfileRepository
import io.nekohasekai.libbox.RoutePrefixIterator
import io.nekohasekai.libbox.TunOptions
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

// The VpnService that owns the tun fd and runs the sing-box engine (via BoxService)
// in-process. It also implements libbox's PlatformInterface through PlatformWrapper,
// overriding only the members that need the service: openTun, the socket-protect
// hook, and libbox notifications.
class TenebraVpnService : VpnService(), PlatformWrapper {

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val repository by lazy { ProfileRepository.getInstance(this) }

    private var boxService: BoxService? = null

    @Volatile
    private var tunFd: ParcelFileDescriptor? = null

    @Volatile
    private var stopping = false

    override fun onCreate() {
        super.onCreate()
        // Track the underlying network for the whole service lifetime so the engine
        // can bind its upstream and follow Wi-Fi <-> cellular handovers.
        DefaultNetworkMonitor.start(this)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                stopTunnel()
                return START_NOT_STICKY
            }

            else -> startTunnel(intent)
        }
        // Not sticky: if the system kills us, do not silently reconnect without the
        // user's tun consent still being valid — the always-on VPN setting is the
        // supported way to auto-restart.
        return START_NOT_STICKY
    }

    private fun startTunnel(intent: Intent?) {
        if (boxService != null) return // already running

        TunnelState.clearError()
        TunnelState.setStatus(TunnelState.Status.Starting)

        // Enter the foreground BEFORE the long work — the system requires the
        // startForeground call promptly after startForegroundService. This runs
        // outside the coroutine's try/catch below, so a throw here (e.g. an OEM/version
        // rejecting the foreground-service type) would be uncaught and kill the whole
        // process. enterForeground swallows that, records the reason, and we bail out
        // cleanly instead.
        if (!enterForeground()) {
            stopTunnel()
            return
        }

        val requestedServerId = intent?.getStringExtra(EXTRA_SELECTED_SERVER_ID)

        scope.launch {
            try {
                val profileJson = repository.currentProfileJson()
                    ?: throw IllegalStateException("No profile imported")

                val serverId = requestedServerId ?: repository.currentSelectedServerId()

                // Resolve the chosen node's selector tag via the core's own node->tag
                // authority: ordering with lastGood = serverId leads the walk with that
                // node, so its tag comes back first. With no selection we pass no tag
                // and let the core pick the first usable node.
                val selectedTag = if (serverId != null) {
                    runCatching {
                        ConfigGenerator.orderNodes(profileJson, lastGood = serverId).firstOrNull()
                    }.getOrNull().orEmpty()
                } else {
                    ""
                }

                val config = ConfigGenerator.generateConfig(
                    rawProfileJson = profileJson,
                    selectedTag = selectedTag.ifEmpty { null },
                )

                val box = BoxService(this@TenebraVpnService, this@TenebraVpnService)
                box.onServiceStop = { stopTunnel() }
                // Boots the engine, which calls back into openTun below for the fd.
                box.start(config)
                boxService = box

                if (serverId != null) repository.setLastGoodServerId(serverId)

                // A libbox callback (e.g. openTun) can fail by recording an error and
                // returning an invalid value rather than throwing, so libbox may not
                // raise here. If one did, do not claim a healthy tunnel.
                val callbackError = TunnelState.lastError.value
                if (!callbackError.isNullOrBlank()) error(callbackError)

                TunnelState.setStatus(TunnelState.Status.Started)
                updateNotification(getString(com.tenebra.android.R.string.notification_connected))
            } catch (t: Throwable) {
                Log.e(TAG, "startTunnel failed", t)
                // Preserve a more specific error a callback already recorded; fall back
                // to this exception's message only when none is set.
                if (TunnelState.lastError.value.isNullOrBlank()) {
                    TunnelState.setError(t.message ?: "Failed to start")
                }
                stopTunnel()
            }
        }
    }

    private fun stopTunnel() {
        if (stopping) return
        stopping = true
        TunnelState.setStatus(TunnelState.Status.Stopping)
        scope.launch {
            runCatching { boxService?.stop() }
            boxService = null
            runCatching { tunFd?.close() }
            tunFd = null
            withContext(Dispatchers.Main) {
                stopForegroundCompat()
                stopSelf()
            }
            TunnelState.setStatus(TunnelState.Status.Stopped)
            stopping = false
        }
    }

    // --- PlatformInterface overrides that need the service ---

    // libbox asks for the tunnel fd. We build the interface from the config's tun
    // options (addresses/mtu/stack) and add routing/DNS ourselves, then establish().
    //
    // This is a libbox callback: it runs on a Go-owned thread, so an exception that
    // escapes it crosses the gomobile boundary, where it can abort the process
    // (SIGABRT) instead of surfacing as a catchable error. Nothing is allowed to throw
    // out of here — every failure is recorded and reported to the engine as an invalid
    // fd (libbox treats fd < 0 as "could not open the tun" and stops).
    override fun openTun(options: TunOptions): Int =
        runCatching { openTunOrThrow(options) }.getOrElse {
            Log.e(TAG, "openTun failed", it)
            TunnelState.setError(it.message ?: "Failed to open the VPN interface")
            -1
        }

    private fun openTunOrThrow(options: TunOptions): Int {
        if (VpnService.prepare(this) != null) {
            error("android: VPN permission is not granted")
        }

        val builder = Builder()
        builder.setSession(SESSION_NAME)

        val mtu = if (options.mtu > 0) options.mtu else DEFAULT_MTU
        builder.setMtu(mtu)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            builder.setMetered(false)
        }

        // The tun's own addresses come from the generated config (present even with
        // externalTun). verify against generated tenebra.aar: modern element getters
        // are methods (address()/prefix()); classic exposes them as properties.
        addAddresses(builder, options.inet4Address)
        addAddresses(builder, options.inet6Address)

        // We own routing (externalTun => no auto_route in the config), so claim the
        // full default route for both families here. sing-box's route rules still send
        // LAN/direct flows out directly; those sockets are protect()'d, so this does
        // not create a loop.
        builder.addRoute("0.0.0.0", 0)
        builder.addRoute("::", 0)

        // Any DNS server works: once 0.0.0.0/0 is in the tun, DNS to it enters the
        // tunnel and sing-box's DNS module answers. Prefer the engine's value if it
        // gave one. verify: modern exposes options.dnsServerAddress.value.
        val dns = runCatching { options.dnsServerAddress?.value }.getOrNull()
        builder.addDnsServer(if (!dns.isNullOrBlank()) dns else DEFAULT_DNS)

        // Tapping the "settings" gear next to the system VPN entry opens our app.
        builder.setConfigureIntent(
            android.app.PendingIntent.getActivity(
                this,
                0,
                Intent(this, com.tenebra.android.ui.MainActivity::class.java),
                android.app.PendingIntent.FLAG_UPDATE_CURRENT or android.app.PendingIntent.FLAG_IMMUTABLE,
            ),
        )

        val pfd = builder.establish()
            ?: error("android: establish() returned null (VPN not prepared or revoked)")
        tunFd = pfd
        // verify: modern SFA returns pfd.fd (keeps the ParcelFileDescriptor to close
        // on stop); it does not detachFd.
        return pfd.fd
    }

    // libbox hands each engine-created socket here so it bypasses the tun. Backs
    // usePlatformAutoDetectInterfaceControl() == true in PlatformWrapper. libbox
    // callback (Go thread): protect() should not throw, but guard it so nothing can
    // escape back into the engine.
    override fun autoDetectInterfaceControl(fd: Int) {
        runCatching { protect(fd) }
            .onFailure { Log.w(TAG, "protect($fd) failed", it) }
    }

    // Engine -> platform notifications (warnings, subscription messages). Surfaced to
    // the log for now. libbox callback (Go thread): reading the fields must not throw
    // back across the boundary. verify: libbox Notification type/fields across versions.
    override fun sendNotification(notification: io.nekohasekai.libbox.Notification) {
        runCatching {
            Log.i(TAG, "libbox notification: ${notification.title} — ${notification.body}")
        }
    }

    private fun addAddresses(builder: Builder, iterator: RoutePrefixIterator?) {
        if (iterator == null) return
        while (iterator.hasNext()) {
            val prefix = iterator.next()
            builder.addAddress(prefix.address(), prefix.prefix())
        }
    }

    // --- lifecycle ---

    // The user revoked the VPN (or another VPN took over). Tear down cleanly.
    override fun onRevoke() {
        stopTunnel()
    }

    override fun onDestroy() {
        DefaultNetworkMonitor.stop()
        scope.cancel()
        super.onDestroy()
    }

    // --- foreground helpers ---

    // Moves the service to the foreground without ever crashing the process. On
    // Android 14+ a VPN uses the SYSTEM_EXEMPTED foreground-service type — the same
    // type SagerNet/sing-box-for-android uses for its VpnService; there is no dedicated
    // "vpn" type. If a particular OEM/version rejects the typed start we retry with the
    // untyped form (which resolves the type from the manifest), and if even that fails
    // we record the reason and give up cleanly rather than let an uncaught exception
    // kill the app. Returns true once the service is in the foreground.
    private fun enterForeground(): Boolean {
        val notification = runCatching {
            ServiceNotification.build(
                this,
                getString(com.tenebra.android.R.string.notification_connecting),
            )
        }.getOrElse {
            Log.e(TAG, "failed to build the foreground notification", it)
            TunnelState.setError(it.message ?: "Failed to build the notification")
            return false
        }

        if (Build.VERSION.SDK_INT >= 34) {
            val typed = runCatching {
                startForeground(
                    ServiceNotification.NOTIFICATION_ID,
                    notification,
                    ServiceInfo.FOREGROUND_SERVICE_TYPE_SYSTEM_EXEMPTED,
                )
            }
            if (typed.isSuccess) return true
            Log.e(TAG, "typed startForeground rejected, retrying untyped", typed.exceptionOrNull())
        }

        return runCatching {
            startForeground(ServiceNotification.NOTIFICATION_ID, notification)
        }.fold(
            onSuccess = { true },
            onFailure = {
                Log.e(TAG, "startForeground failed", it)
                TunnelState.setError(
                    "The OS refused to start the VPN foreground service: " +
                        "${it.javaClass.simpleName}: ${it.message}",
                )
                false
            },
        )
    }

    private fun stopForegroundCompat() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.N) {
            stopForeground(STOP_FOREGROUND_REMOVE)
        } else {
            @Suppress("DEPRECATION")
            stopForeground(true)
        }
    }

    private fun updateNotification(text: String) {
        val manager = getSystemService(Context.NOTIFICATION_SERVICE) as android.app.NotificationManager
        manager.notify(ServiceNotification.NOTIFICATION_ID, ServiceNotification.build(this, text))
    }

    companion object {
        private const val TAG = "TenebraVpn"
        private const val SESSION_NAME = "Tenebra"
        private const val DEFAULT_MTU = 1500
        private const val DEFAULT_DNS = "1.1.1.1"

        const val ACTION_START = "com.tenebra.android.START"
        const val ACTION_STOP = "com.tenebra.android.STOP"
        const val EXTRA_SELECTED_SERVER_ID = "selected_server_id"

        // Start the tunnel. Consent (VpnService.prepare) must already be granted —
        // the caller (MainActivity) obtains it before invoking this.
        fun start(context: Context, selectedServerId: String?) {
            val intent = Intent(context, TenebraVpnService::class.java)
                .setAction(ACTION_START)
                .putExtra(EXTRA_SELECTED_SERVER_ID, selectedServerId)
            ContextCompat.startForegroundService(context, intent)
        }

        fun stop(context: Context) {
            val intent = Intent(context, TenebraVpnService::class.java).setAction(ACTION_STOP)
            context.startService(intent)
        }
    }
}
