package com.tenebra.android.ui

import android.app.Application
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import com.tenebra.android.BuildConfig
import com.tenebra.android.CrashLog
import com.tenebra.android.R
import com.tenebra.android.bg.DiagnosticsReport
import com.tenebra.android.bg.DiagnosticsScrubber
import com.tenebra.android.bg.LogStore
import com.tenebra.android.bg.TenebraVpnService
import com.tenebra.android.bg.TunnelState
import com.tenebra.android.core.ConfigGenerator
import com.tenebra.android.core.TenebraProfile
import com.tenebra.android.net.ClashApiClient
import com.tenebra.android.net.NodePinger
import com.tenebra.android.store.ProfileRepository
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.sample
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.serialization.decodeFromString
import kotlinx.serialization.json.Json

// Holds the screen's state and the import/selection actions. Connect/disconnect that
// need an Activity (the VpnService consent dialog) stay in MainActivity; everything
// else lives here. The tunnel status is read straight from the process-wide
// TunnelState the service updates.
class MainViewModel(application: Application) : AndroidViewModel(application) {

    private val repository = ProfileRepository.getInstance(application)
    private val json = Json { ignoreUnknownKeys = true }

    val profile: StateFlow<TenebraProfile?> = repository.profileJson
        .map { raw -> raw?.let { runCatching { json.decodeFromString<TenebraProfile>(it) }.getOrNull() } }
        .stateIn(viewModelScope, SharingStarted.WhileSubscribed(5_000), null)

    val status: StateFlow<TunnelState.Status> = TunnelState.status
    val tunnelError: StateFlow<String?> = TunnelState.lastError
    val selectedServerId: StateFlow<String?> = repository.selectedServerId
    val autoMode: StateFlow<Boolean> = repository.autoMode

    private val _isImporting = MutableStateFlow(false)
    val isImporting: StateFlow<Boolean> = _isImporting.asStateFlow()

    private val _importError = MutableStateFlow<String?>(null)
    val importError: StateFlow<String?> = _importError.asStateFlow()

    // Node latency badges: id -> connect-time ms (-1 = no answer). Empty until the first
    // probe. refreshPings() is cheap to call again (e.g. pull-to-refresh); a probe
    // already in flight is ignored so taps don't stack sweeps.
    private val _pings = MutableStateFlow<Map<String, Int>>(emptyMap())
    val pings: StateFlow<Map<String, Int>> = _pings.asStateFlow()
    private val _isPinging = MutableStateFlow(false)
    val isPinging: StateFlow<Boolean> = _isPinging.asStateFlow()

    // Node id -> selector tag (from the core's own authority), so a tap can steer the
    // live tunnel to that node with no reconnect. Refreshed when the profile changes.
    private val _tags = MutableStateFlow<Map<String, String>>(emptyMap())

    // The stack trace of the last uncaught JVM crash, if any, read once at launch.
    // Shown to the user so a crash can be reported without adb; cleared on demand.
    private val _lastCrash = MutableStateFlow(CrashLog.read(application))
    val lastCrash: StateFlow<String?> = _lastCrash.asStateFlow()

    // The diagnostics log for the Logs screen. It is throttled on purpose: libbox
    // forwards every engine line to the platform, so during load the buffer can churn
    // fast — sampling the revision counter and pulling a snapshot at most a few times a
    // second keeps that off the main thread as a per-line list copy. WhileSubscribed
    // means it costs nothing while the Logs screen is closed.
    val logLines: StateFlow<List<LogStore.Line>> = LogStore.revision
        .sample(200)
        .map { LogStore.snapshot() }
        .stateIn(viewModelScope, SharingStarted.WhileSubscribed(5_000), LogStore.snapshot())

    fun importSubscription(url: String) {
        val trimmed = url.trim()
        if (trimmed.isEmpty() || _isImporting.value) return
        _importError.value = null
        _isImporting.value = true
        viewModelScope.launch {
            try {
                LogStore.i("import", "importing subscription")
                val profileJson = withContext(Dispatchers.IO) {
                    ConfigGenerator.importSubscription(trimmed)
                }
                repository.saveProfile(profileJson)
                LogStore.i("import", "subscription imported")
                refreshTags()
            } catch (t: Throwable) {
                // Goes into the diagnostics log too — a failed import is exactly the kind
                // of thing a tester needs to report. Any URL token in the message is
                // masked by the scrubber before the log is shared.
                LogStore.e("import", "import failed", t)
                _importError.value = friendlyImportError(t)
            } finally {
                _isImporting.value = false
            }
        }
    }

    fun selectNode(serverId: String) {
        repository.setAutoMode(false)
        repository.setSelectedServerId(serverId)
        hotSwitch(serverId)
    }

    // Turn on AUTO: keep the selection on the fastest node by ping, re-picking as pings
    // refresh. Applies immediately (and switches the live tunnel if one is up).
    fun selectAuto() {
        repository.setAutoMode(true)
        applyAuto()
    }

    private fun applyAuto() {
        val fastest = fastestNodeId() ?: return
        repository.setSelectedServerId(fastest)
        hotSwitch(fastest)
    }

    // The reachable node with the lowest ping, or the first node when nothing has
    // answered yet — so AUTO always resolves to something usable.
    fun fastestNodeId(): String? {
        val reachable = _pings.value.filterValues { it > 0 }
        return reachable.minByOrNull { it.value }?.key
            ?: profile.value?.nodes?.firstOrNull()?.id
    }

    // Steer the running tunnel to the chosen node immediately (no reconnect) when it is
    // up. While disconnected this is a no-op — the saved selection applies on connect.
    // The switch is logged so a tester can see it took (or why it didn't).
    private fun hotSwitch(serverId: String) {
        if (status.value != TunnelState.Status.Started) return
        val tag = _tags.value[serverId]
        if (tag == null) {
            LogStore.w("switch", "no tag for $serverId (have ${_tags.value.size} tags)")
            return
        }
        viewModelScope.launch {
            val ok = ClashApiClient.selectOutbound(tag)
            LogStore.i("switch", "hot-switch to $tag: ${if (ok) "ok" else "failed"}")
        }
    }

    // Refresh the id -> selector tag map for the current profile (off the main thread).
    fun refreshTags() {
        val raw = repository.currentProfileJson() ?: return
        viewModelScope.launch {
            val tags = withContext(Dispatchers.IO) {
                runCatching { ConfigGenerator.nodeTags(raw) }.getOrElse {
                    LogStore.e("tags", "nodeTags failed", it)
                    emptyMap()
                }
            }
            _tags.value = tags
            LogStore.i("tags", "loaded ${tags.size} node tags")
        }
    }

    // Probe every node's connect-time latency, concurrently and off the main thread.
    // Safe to call repeatedly; a sweep already running is left to finish.
    fun refreshPings() {
        if (_isPinging.value) return
        val nodes = profile.value?.nodes ?: return
        if (nodes.isEmpty()) return
        _isPinging.value = true
        viewModelScope.launch {
            try {
                val targets = nodes.map { NodePinger.Target(it.id, it.server, it.port) }
                _pings.value = withContext(Dispatchers.IO) { NodePinger.pingAll(targets) }
                // In AUTO, a fresh set of latencies may crown a new fastest node — follow it.
                if (repository.currentAutoMode()) applyAuto()
            } finally {
                _isPinging.value = false
            }
        }
    }

    // The current selection, read synchronously for the connect path in the activity.
    fun currentSelectedServerId(): String? = repository.currentSelectedServerId()

    fun hasProfile(): Boolean = repository.currentProfileJson() != null

    fun disconnect() {
        TenebraVpnService.stop(getApplication())
    }

    fun clearCrashLog() {
        CrashLog.clear(getApplication())
        _lastCrash.value = null
    }

    // The exact text the user copies or shares from the Logs screen: header + last
    // crash + captured log, run through the scrubber so subscription tokens, node
    // credentials and server addresses never leave the device. Nothing here touches the
    // network — the report only goes where the user's own Share/Copy sends it.
    fun buildDiagnosticsReport(): String {
        val report = DiagnosticsReport.compose(
            header = DiagnosticsReport.header(),
            lastCrash = CrashLog.read(getApplication()),
            lines = LogStore.snapshot(),
        )
        val secrets = DiagnosticsScrubber.secretsFromProfileJson(repository.currentProfileJson())
        return DiagnosticsScrubber.scrub(report, secrets)
    }

    // Wipe the in-memory log and the saved crash file together, so "Clear" on the Logs
    // screen leaves nothing behind to send.
    fun clearDiagnostics() {
        LogStore.clear()
        CrashLog.clear(getApplication())
        _lastCrash.value = null
    }

    fun dismissImportError() {
        _importError.value = null
    }

    // --- settings ---

    val autoConnect: StateFlow<Boolean> = repository.autoConnect

    fun setAutoConnect(enabled: Boolean) = repository.setAutoConnect(enabled)

    // Re-fetch the stored subscription URL to refresh the node set. Reuses the import
    // path (spinner + friendly errors), so the Settings screen shows the same state.
    fun refreshSubscription() {
        val url = profile.value?.url.orEmpty()
        if (url.isNotBlank()) importSubscription(url)
    }

    fun removeProfile() = repository.clearProfile()

    fun appVersion(): String = "${BuildConfig.VERSION_NAME} (${BuildConfig.VERSION_CODE})"

    fun coreVersion(): String = runCatching { com.tenebra.android.core.ConfigGenerator.version() }.getOrDefault("—")

    fun engineVersion(): String = runCatching { io.nekohasekai.libbox.Libbox.version() }.getOrDefault("—")

    // Map the core's raw import error (an engineer-facing Go string) to a message in the
    // app's own voice. The raw text still goes to the diagnostics log for a bug report;
    // this is only what the user sees under the import field.
    private fun friendlyImportError(t: Throwable): String {
        val app = getApplication<Application>()
        val msg = (t.message ?: "").lowercase()
        return when {
            "scheme" in msg || ("http" in msg && "https" in msg) ->
                app.getString(R.string.import_err_bad_url)
            "no usable nodes" in msg || "no nodes" in msg || "empty" in msg ->
                app.getString(R.string.import_err_empty)
            "timeout" in msg || "no such host" in msg || "connection" in msg ||
                "dial" in msg || "eof" in msg || "network" in msg || "refused" in msg ->
                app.getString(R.string.import_err_network)
            else -> app.getString(R.string.import_err_generic)
        }
    }
}
