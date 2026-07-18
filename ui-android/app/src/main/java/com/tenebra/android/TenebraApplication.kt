package com.tenebra.android

import android.app.Application
import com.tenebra.android.bg.ServiceNotification

// Process entry point. Kept thin: it only creates the notification channel up front
// (cheap, idempotent) so the foreground service can post immediately when it starts.
// Any libbox one-time setup is deliberately NOT here — it belongs in the tunnel
// process/service, close to where the engine runs, not in every app launch.
class TenebraApplication : Application() {
    override fun onCreate() {
        super.onCreate()
        // Record any uncaught JVM crash to a file the UI reads on next launch, so a
        // tester without adb/logcat can send back the cause. Installed first so it
        // covers the rest of startup too.
        CrashLog.install(this)
        ServiceNotification.createChannel(this)
    }
}
