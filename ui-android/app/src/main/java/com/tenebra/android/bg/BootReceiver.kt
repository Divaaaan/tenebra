package com.tenebra.android.bg

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.net.VpnService
import com.tenebra.android.store.ProfileRepository

// Brings the tunnel up on device boot, but only when the user asked for it AND VPN
// consent is already granted. A receiver has no Activity to prompt from, so if consent
// is missing we skip silently rather than fail — the user grants it once by connecting
// by hand, and from then on boot-connect works.
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED) return
        val repo = ProfileRepository.getInstance(context)
        if (!repo.currentAutoConnect()) return
        if (repo.currentProfileJson() == null) return
        // prepare() returns null only when consent already exists; a non-null intent
        // would need an Activity we don't have here.
        if (VpnService.prepare(context) != null) return
        TenebraVpnService.start(context, repo.currentSelectedServerId())
    }
}
