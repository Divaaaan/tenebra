package com.tenebra.android.bg

import android.os.Build
import com.tenebra.android.BuildConfig
import com.tenebra.android.core.TenebraCore
import io.nekohasekai.libbox.Libbox
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

// Assembles the plain-text diagnostic report: a short header (app/OS/device/engine),
// the last crash if one was recorded, and the captured log lines. This is the exact
// text the user copies or shares — it is run through DiagnosticsScrubber before it
// leaves the device.
object DiagnosticsReport {

    // Wall-clock time per line. The engine only stamps its lines with seconds since its
    // own start, so an absolute time here is what makes the log readable after the fact.
    private val timeFormat = object : ThreadLocal<SimpleDateFormat>() {
        override fun initialValue() = SimpleDateFormat("HH:mm:ss.SSS", Locale.US)
    }

    fun header(): String {
        val engine = runCatching { Libbox.version() }.getOrNull()
        val core = runCatching { TenebraCore.version() }.getOrNull()
        return buildString {
            appendLine("Tenebra diagnostics")
            appendLine("app: ${BuildConfig.VERSION_NAME} (${BuildConfig.VERSION_CODE})")
            appendLine("android: ${Build.VERSION.RELEASE} (API ${Build.VERSION.SDK_INT})")
            appendLine("device: ${Build.MANUFACTURER} ${Build.MODEL}")
            if (!core.isNullOrBlank()) appendLine("core: $core")
            if (!engine.isNullOrBlank()) appendLine("engine: sing-box $engine")
        }
    }

    // The full report, ready to scrub and share. Header + last crash + log.
    fun compose(header: String, lastCrash: String?, lines: List<LogStore.Line>): String =
        buildString {
            append(header)
            if (!lastCrash.isNullOrBlank()) {
                appendLine()
                appendLine("--- last crash ---")
                appendLine(lastCrash.trim())
            }
            appendLine()
            appendLine("--- log (${lines.size} lines) ---")
            if (lines.isEmpty()) {
                appendLine("(no log lines captured yet)")
            } else {
                lines.forEach { appendLine(formatLine(it)) }
            }
        }

    fun formatLine(line: LogStore.Line): String =
        "${timeFormat.get()!!.format(Date(line.timeMillis))} ${levelTag(line.level)} ${line.text}"

    private fun levelTag(level: LogStore.Level): String = when (level) {
        LogStore.Level.Debug -> "D"
        LogStore.Level.Info -> "I"
        LogStore.Level.Warn -> "W"
        LogStore.Level.Error -> "E"
    }
}
