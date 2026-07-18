package com.tenebra.android

import android.content.Context
import android.os.Build
import com.tenebra.android.bg.DiagnosticsReport
import com.tenebra.android.bg.LogStore
import java.io.File
import java.io.PrintWriter
import java.io.StringWriter

// Records the last uncaught JVM exception to a file so a tester with no adb/logcat can
// read (and send back) the cause from inside the app.
//
// IMPORTANT LIMITATION: this only captures JVM crashes — an uncaught exception on any
// thread, including exceptions thrown out of the VpnService/coroutines. A pure native
// abort from the Go/libbox runtime (SIGABRT/SIGSEGV) does NOT unwind through the JVM
// and cannot be caught here. Once the connect path has been made exception-safe, the
// app dying WITHOUT leaving a report is itself the signal that the fault is native.
object CrashLog {

    private const val FILE_NAME = "last_crash.txt"

    // Chains a recording handler ahead of the platform default so the crash is still
    // delivered to the system (the process still dies) — this only observes it.
    fun install(context: Context) {
        val appContext = context.applicationContext
        val previous = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { thread, throwable ->
            runCatching { write(appContext, thread, throwable) }
            previous?.uncaughtException(thread, throwable)
        }
    }

    fun read(context: Context): String? {
        val file = File(context.filesDir, FILE_NAME)
        if (!file.exists()) return null
        return runCatching { file.readText() }.getOrNull()?.ifBlank { null }
    }

    fun clear(context: Context) {
        runCatching { File(context.filesDir, FILE_NAME).delete() }
    }

    private fun write(context: Context, thread: Thread, throwable: Throwable) {
        val trace = StringWriter().also { sw ->
            PrintWriter(sw).use { pw ->
                pw.println("Tenebra crash report")
                pw.println("when: ${System.currentTimeMillis()} (epoch ms)")
                pw.println("thread: ${thread.name}")
                pw.println("device: ${Build.MANUFACTURER} ${Build.MODEL}")
                pw.println("android: ${Build.VERSION.RELEASE} (API ${Build.VERSION.SDK_INT})")
                pw.println("app: ${BuildConfig.VERSION_NAME} (${BuildConfig.VERSION_CODE})")
                pw.println()
                throwable.printStackTrace(pw)
                // Fold in the recent in-memory log so a crash carries the engine/app
                // lines that led up to it. The buffer dies with the process, and a JVM
                // crash always forces a restart, so this run's live buffer is empty by
                // the time the report is read back — no risk of duplicating it there.
                val recent = runCatching { LogStore.snapshot() }.getOrNull().orEmpty()
                if (recent.isNotEmpty()) {
                    pw.println()
                    pw.println("--- recent log (last ${minOf(recent.size, 200)} lines) ---")
                    recent.takeLast(200).forEach { pw.println(DiagnosticsReport.formatLine(it)) }
                }
            }
        }.toString()
        File(context.filesDir, FILE_NAME).writeText(trace)
    }
}
