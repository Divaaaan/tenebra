package com.tenebra.android.bg

import android.util.Log
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

// Process-wide in-memory ring buffer for diagnostics. The engine's own output and the
// service's key events land here so a tester with no adb/logcat can read them on the
// Diagnostics screen and send them back with one tap.
//
// PRIVACY: this buffer is LOCAL and never leaves the device on its own. It goes out
// only when the user taps Share/Copy on the Diagnostics screen, after scrubbing (see
// DiagnosticsScrubber). Tenebra ships zero telemetry — keep it that way.
object LogStore {

    enum class Level { Debug, Info, Warn, Error }

    data class Line(val timeMillis: Long, val level: Level, val text: String)

    // ~1000 lines covers a connect attempt and its aftermath while staying inside a few
    // hundred KB of heap. Because libbox forwards every engine line to the platform
    // regardless of the config log level, this can fill fast under load — the oldest
    // lines simply fall off the front.
    private const val MAX_LINES = 1000

    private val lock = Any()
    private val lines = ArrayDeque<Line>(MAX_LINES)

    // Bumped on every append. The UI observes this and pulls a snapshot at a throttled
    // rate (see MainViewModel.logLines), so a burst of engine lines can never turn into
    // a per-line list copy on the main thread. Guarded by the same lock so the counter
    // and the buffer can't drift.
    private var revisionCounter = 0
    private val _revision = MutableStateFlow(0)
    val revision: StateFlow<Int> = _revision.asStateFlow()

    fun append(level: Level, text: String) {
        if (text.isEmpty()) return
        synchronized(lock) {
            if (lines.size >= MAX_LINES) lines.removeFirst()
            lines.addLast(Line(System.currentTimeMillis(), level, text))
            _revision.value = ++revisionCounter
        }
    }

    // App-side event helpers. Each also mirrors to logcat, so the adb path is unchanged
    // for anyone who does have a cable — this only adds the in-app copy.
    fun d(tag: String, msg: String) {
        Log.d(tag, msg)
        append(Level.Debug, "$tag: $msg")
    }

    fun i(tag: String, msg: String) {
        Log.i(tag, msg)
        append(Level.Info, "$tag: $msg")
    }

    fun w(tag: String, msg: String) {
        Log.w(tag, msg)
        append(Level.Warn, "$tag: $msg")
    }

    fun e(tag: String, msg: String, t: Throwable? = null) {
        Log.e(tag, msg, t)
        append(Level.Error, if (t != null) "$tag: $msg — $t" else "$tag: $msg")
    }

    fun snapshot(): List<Line> = synchronized(lock) { lines.toList() }

    fun clear() {
        synchronized(lock) {
            lines.clear()
            _revision.value = ++revisionCounter
        }
    }
}
