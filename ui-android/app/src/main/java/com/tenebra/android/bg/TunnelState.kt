package com.tenebra.android.bg

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

// Process-wide tunnel state. The VpnService/BoxService run in the app's main process
// (no separate :box process — Android imposes no iOS-style memory cap, so the split
// is unnecessary), which lets the UI observe this StateFlow directly instead of an
// AIDL callback. If the service is ever moved to its own process, replace this with
// the bound-service + IServiceCallback pattern SFA uses.
object TunnelState {

    enum class Status { Stopped, Starting, Started, Stopping }

    private val _status = MutableStateFlow(Status.Stopped)
    val status: StateFlow<Status> = _status.asStateFlow()

    // Last fatal error, surfaced to the UI. Cleared on the next start attempt.
    private val _lastError = MutableStateFlow<String?>(null)
    val lastError: StateFlow<String?> = _lastError.asStateFlow()

    fun setStatus(status: Status) {
        _status.value = status
    }

    fun setError(message: String?) {
        _lastError.value = message
    }

    fun clearError() {
        _lastError.value = null
    }
}
