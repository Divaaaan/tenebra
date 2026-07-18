package com.tenebra.android.ui

import android.app.Application
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import com.tenebra.android.CrashLog
import com.tenebra.android.bg.DiagnosticsReport
import com.tenebra.android.bg.DiagnosticsScrubber
import com.tenebra.android.bg.LogStore
import com.tenebra.android.bg.TenebraVpnService
import com.tenebra.android.bg.TunnelState
import com.tenebra.android.core.ConfigGenerator
import com.tenebra.android.core.TenebraProfile
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

    private val _isImporting = MutableStateFlow(false)
    val isImporting: StateFlow<Boolean> = _isImporting.asStateFlow()

    private val _importError = MutableStateFlow<String?>(null)
    val importError: StateFlow<String?> = _importError.asStateFlow()

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
            } catch (t: Throwable) {
                // Goes into the diagnostics log too — a failed import is exactly the kind
                // of thing a tester needs to report. Any URL token in the message is
                // masked by the scrubber before the log is shared.
                LogStore.e("import", "import failed", t)
                _importError.value = t.message ?: "Import failed"
            } finally {
                _isImporting.value = false
            }
        }
    }

    fun selectNode(serverId: String) {
        repository.setSelectedServerId(serverId)
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
}
