// Scaffold: not compiled on the authoring host (no Android SDK / tenebra.aar).
// Mirrors io.nekohasekai.sfa.bg.BoxService from SagerNet/sing-box-for-android
// (GPL-3.0). Tenebra is GPL-3.0-compatible.
// Upstream: https://github.com/SagerNet/sing-box-for-android
//
// Targets the MODERN libbox surface (sing-box ~1.13): the engine lifecycle is driven
// through CommandServer.startOrReloadService rather than the classic
// Libbox.newService. Points that differ across versions are flagged
// "verify against generated tenebra.aar".
package com.tenebra.android.bg

import android.app.Service
import android.util.Log
import io.nekohasekai.libbox.CommandServer
import io.nekohasekai.libbox.CommandServerHandler
import io.nekohasekai.libbox.Libbox
import io.nekohasekai.libbox.OverrideOptions
import io.nekohasekai.libbox.PlatformInterface
import io.nekohasekai.libbox.SetupOptions
import io.nekohasekai.libbox.SystemProxyStatus

// Drives the sing-box engine for one tunnel. The owning VpnService supplies itself
// (for base/temp paths) and the PlatformInterface libbox calls back into. This class
// owns the libbox CommandServer and translates its handler callbacks; it does NOT
// touch the foreground notification or the tun fd — those belong to the service.
class BoxService(
    private val service: Service,
    private val platform: PlatformInterface,
) : CommandServerHandler {

    private var commandServer: CommandServer? = null

    // Invoked when the engine itself asks the service to stop (fatal error or a
    // libbox-initiated stop). The service wires this to its own teardown.
    var onServiceStop: (() -> Unit)? = null

    // Bring the engine up with an already-generated sing-box config. Blocking; the
    // caller runs it off the main thread. Throws on failure so the service can tear
    // the tunnel down cleanly rather than leaving a half-open fd.
    @Throws(Exception::class)
    fun start(config: String) {
        setupOnce()
        val server = CommandServer(this, platform)
        server.start()
        // verify against generated tenebra.aar: modern libbox boots the engine via
        // CommandServer.startOrReloadService(config, OverrideOptions); classic used
        // Libbox.newService(config, platform) + service.start() + setService.
        server.startOrReloadService(config, OverrideOptions())
        commandServer = server
    }

    fun stop() {
        val server = commandServer ?: return
        // closeService stops the running box; close tears down the command server.
        runCatching { server.closeService() }
            .onFailure { Log.w(TAG, "closeService failed", it) }
        runCatching { server.close() }
            .onFailure { Log.w(TAG, "commandServer close failed", it) }
        commandServer = null
    }

    // Device sleep/wake. Modern libbox pauses on the command server (classic paused
    // the box handle directly). Cuts idle timers and connections while dozing.
    fun pause() {
        runCatching { commandServer?.pause() }
    }

    fun wake() {
        runCatching { commandServer?.wake() }
    }

    // --- CommandServerHandler (modern surface) ---

    // All CommandServerHandler methods below are libbox callbacks invoked on a
    // Go-owned thread. An exception that escapes any of them crosses the gomobile
    // boundary, where it can abort the process rather than surface as a catchable
    // error — so each guards its body and returns a safe default.

    override fun serviceReload() {
        // The minimal client keeps one static config per connect; a runtime node
        // switch goes through libbox SelectOutbound, not a reload. Nothing to rebuild
        // here, so acknowledge without action. A full client regenerates the config
        // and calls startOrReloadService again.
        runCatching { Log.d(TAG, "serviceReload (no-op in minimal client)") }
    }

    override fun serviceStop() {
        runCatching { onServiceStop?.invoke() }
            .onFailure { Log.w(TAG, "serviceStop handler failed", it) }
    }

    override fun getSystemProxyStatus(): SystemProxyStatus =
        // This client does not register a system HTTP proxy; report it unavailable.
        // verify field names against generated tenebra.aar.
        runCatching {
            SystemProxyStatus().apply {
                available = false
                enabled = false
            }
        }.getOrElse { SystemProxyStatus() }

    override fun setSystemProxyEnabled(isEnabled: Boolean) {
        // No system proxy to toggle.
    }

    // The engine's own log. libbox only calls this once SetupOptions.debug is on (see
    // setupOnce); every sing-box warning/error/notice arrives here. Strip the ANSI
    // colour codes the engine formatter adds and file it in LogStore so the Diagnostics
    // screen can show it — without this the log of a failed connect is lost to logcat.
    override fun writeDebugMessage(message: String?) {
        runCatching {
            val text = message?.let(::stripAnsi)?.takeIf { it.isNotEmpty() } ?: return
            Log.d(TAG, text)
            LogStore.append(sniffLevel(text), text)
        }
    }

    // Libbox.setup must run once per process before any engine call. Idempotent.
    private fun setupOnce() {
        if (setupDone) return
        synchronized(BoxService::class.java) {
            if (setupDone) return
            runCatching {
                Libbox.setLocale(
                    java.util.Locale.getDefault().toLanguageTag().replace('-', '_'),
                )
            }
            // verify against generated tenebra.aar: modern libbox takes a SetupOptions
            // object; classic took four positional args (base, working, temp, debug).
            val base = service.filesDir.absolutePath
            val working = (service.getExternalFilesDir(null) ?: service.filesDir).absolutePath
            val temp = service.cacheDir.absolutePath
            Libbox.setup(
                SetupOptions().apply {
                    basePath = base
                    workingPath = working
                    tempPath = temp
                    // Fixes a Go-runtime/Android stack interaction; SFA sets it true.
                    fixAndroidStack = true
                    // Route the engine's log to writeDebugMessage. libbox gates the
                    // platform debug hook on this flag (the daemon only forwards a line
                    // when it is set), so without it every sing-box warning/error is
                    // dropped and a failed connect is mute in-app. SFA ties this to
                    // BuildConfig.DEBUG; we keep it on for alpha builds too, because the
                    // whole point is that a tester with no adb still gets engine logs.
                    // The output stays local (LogStore) and is never auto-sent.
                    debug = true
                    // Caps the daemon's own line ring (the gRPC log stream); harmless
                    // here since we capture via writeDebugMessage, matched to SFA.
                    logMaxLines = 3000
                },
            )
            setupDone = true
        }
    }

    companion object {
        private const val TAG = "TenebraBox"

        @Volatile
        private var setupDone = false
    }
}

// The engine's log formatter colours each line with ANSI escapes (its platform
// formatter leaves colours on); strip them so the text is clean on screen and in a
// shared report.
private val ANSI_ESCAPE = Regex("\u001B\\[[0-9;]*m")

private fun stripAnsi(message: String): String = ANSI_ESCAPE.replace(message, "")

// Each engine line begins with its own level word ("INFO[..", "WARN ..", "ERROR ..").
// Read it so the Diagnostics screen can colour the line; fall back to Info for any
// shape we do not recognise so nothing is mis-flagged as an error.
private fun sniffLevel(message: String): LogStore.Level {
    val head = message.trimStart().takeWhile { it.isLetter() }.uppercase()
    return when (head) {
        "PANIC", "FATAL", "ERROR" -> LogStore.Level.Error
        "WARN", "WARNING" -> LogStore.Level.Warn
        "DEBUG", "TRACE" -> LogStore.Level.Debug
        else -> LogStore.Level.Info
    }
}
