// Scaffold: not compiled on the authoring host (no Android SDK). A Quick Settings
// tile mirroring SFA's QSTileService (SagerNet/sing-box-for-android, GPL-3.0),
// trimmed to connect/disconnect. Tenebra is GPL-3.0-compatible.
// Upstream: https://github.com/SagerNet/sing-box-for-android
package com.tenebra.android.bg

import android.app.PendingIntent
import android.content.Intent
import android.net.VpnService
import android.os.Build
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService
import com.tenebra.android.store.ProfileRepository
import com.tenebra.android.ui.MainActivity

// One-tap tunnel toggle in the notification shade. Consent and the "no profile yet"
// case can't be resolved from a tile, so it defers to the app for those and only
// toggles directly when it safely can.
class TenebraTileService : TileService() {

    override fun onStartListening() {
        super.onStartListening()
        refresh()
    }

    override fun onClick() {
        super.onClick()
        when (TunnelState.status.value) {
            TunnelState.Status.Started,
            TunnelState.Status.Starting,
            -> {
                TenebraVpnService.stop(this)
            }

            else -> connectOrDeferToApp()
        }
        refresh()
    }

    private fun connectOrDeferToApp() {
        val repository = ProfileRepository.getInstance(this)
        val hasProfile = repository.currentProfileJson() != null
        val consentNeeded = VpnService.prepare(this) != null

        if (hasProfile && !consentNeeded) {
            TenebraVpnService.start(this, repository.currentSelectedServerId())
        } else {
            // Need import and/or the consent dialog — both live in the activity.
            openApp()
        }
    }

    private fun openApp() {
        val intent = Intent(this, MainActivity::class.java)
            .setFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP)
        if (Build.VERSION.SDK_INT >= 34) {
            val pending = PendingIntent.getActivity(
                this,
                0,
                intent,
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
            )
            startActivityAndCollapse(pending)
        } else {
            @Suppress("DEPRECATION", "StartActivityAndCollapseDeprecated")
            startActivityAndCollapse(intent)
        }
    }

    private fun refresh() {
        val tile = qsTile ?: return
        val active = when (TunnelState.status.value) {
            TunnelState.Status.Started, TunnelState.Status.Starting -> true
            else -> false
        }
        tile.state = if (active) Tile.STATE_ACTIVE else Tile.STATE_INACTIVE
        tile.label = getString(com.tenebra.android.R.string.tile_label)
        tile.updateTile()
    }
}
