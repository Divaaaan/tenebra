package com.tenebra.android.ui

import android.Manifest
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.lifecycle.ViewModelProvider
import com.tenebra.android.bg.TenebraVpnService
import com.tenebra.android.ui.theme.TenebraTheme

// The single activity. It hosts the Compose UI and owns the two things that need an
// Activity: the VpnService consent dialog and the POST_NOTIFICATIONS request.
class MainActivity : ComponentActivity() {

    private lateinit var viewModel: MainViewModel

    // VpnService.prepare returns an intent when the user has not yet granted this app
    // the VPN capability; launching it shows the system consent dialog. On approval we
    // start the tunnel; on refusal we do nothing.
    private val vpnConsent = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult(),
    ) { result ->
        if (result.resultCode == RESULT_OK) startTunnel()
    }

    private val notificationPermission = registerForActivityResult(
        ActivityResultContracts.RequestPermission(),
    ) { /* the tunnel runs regardless; the notification is just suppressed if denied */ }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        viewModel = ViewModelProvider(this)[MainViewModel::class.java]
        requestNotificationPermissionIfNeeded()
        setContent {
            TenebraTheme {
                MainScreen(
                    viewModel = viewModel,
                    onConnect = ::connect,
                    onDisconnect = viewModel::disconnect,
                )
            }
        }
    }

    private fun connect() {
        if (!viewModel.hasProfile()) return
        val consent = VpnService.prepare(this)
        if (consent != null) {
            vpnConsent.launch(consent)
        } else {
            startTunnel()
        }
    }

    private fun startTunnel() {
        TenebraVpnService.start(this, viewModel.currentSelectedServerId())
    }

    private fun requestNotificationPermissionIfNeeded() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return
        val granted = checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) ==
            PackageManager.PERMISSION_GRANTED
        if (!granted) {
            notificationPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
    }
}
