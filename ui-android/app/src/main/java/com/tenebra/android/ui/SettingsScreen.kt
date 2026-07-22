package com.tenebra.android.ui

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchDefaults
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.tenebra.android.R
import com.tenebra.android.ui.theme.TenebraPalette
import java.util.Locale

// The second-level screen: boot-connect, subscription upkeep, and version info. Reached
// from the main screen's top bar; handles its own system-back like the logs screen.
@Composable
fun SettingsScreen(
    viewModel: MainViewModel,
    onBack: () -> Unit,
) {
    BackHandler(onBack = onBack)
    val profile by viewModel.profile.collectAsState()
    val autoConnect by viewModel.autoConnect.collectAsState()
    val isImporting by viewModel.isImporting.collectAsState()
    val importError by viewModel.importError.collectAsState()

    Column(
        Modifier
            .fillMaxSize()
            .background(TenebraPalette.ground)
            .verticalScroll(rememberScrollState()),
    ) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            modifier = Modifier
                .fillMaxWidth()
                .padding(start = 8.dp, end = 18.dp, top = 14.dp, bottom = 6.dp),
        ) {
            TextButton(onClick = onBack) {
                Text(
                    text = stringResource(R.string.settings_back),
                    style = MaterialTheme.typography.labelMedium,
                    color = TenebraPalette.signal,
                )
            }
            Spacer(Modifier.width(4.dp))
            Text(
                text = stringResource(R.string.settings).uppercase(),
                style = MaterialTheme.typography.titleLarge,
                color = TenebraPalette.text,
            )
        }

        Section(stringResource(R.string.settings_autoconnect).uppercase()) {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                modifier = Modifier.fillMaxWidth().padding(16.dp),
            ) {
                Column(Modifier.weight(1f)) {
                    Text(
                        text = stringResource(R.string.settings_autoconnect),
                        style = MaterialTheme.typography.bodyMedium,
                        color = TenebraPalette.text,
                    )
                    Spacer(Modifier.height(2.dp))
                    Text(
                        text = stringResource(R.string.settings_autoconnect_desc),
                        style = MaterialTheme.typography.bodySmall,
                        color = TenebraPalette.dim,
                    )
                }
                Spacer(Modifier.width(12.dp))
                Switch(
                    checked = autoConnect,
                    onCheckedChange = viewModel::setAutoConnect,
                    colors = SwitchDefaults.colors(
                        checkedThumbColor = TenebraPalette.ground,
                        checkedTrackColor = TenebraPalette.signal,
                        checkedBorderColor = TenebraPalette.signal,
                        uncheckedThumbColor = TenebraPalette.dim,
                        uncheckedTrackColor = TenebraPalette.surface,
                        uncheckedBorderColor = TenebraPalette.border,
                    ),
                )
            }
        }

        profile?.let { p ->
            Section(stringResource(R.string.settings_subscription).uppercase()) {
                Column(Modifier.fillMaxWidth().padding(16.dp)) {
                    if (p.updatedAt.isNotBlank()) {
                        Text(
                            text = stringResource(R.string.settings_updated, p.updatedAt.substringBefore('T')),
                            style = MaterialTheme.typography.bodySmall,
                            color = TenebraPalette.dim,
                        )
                    }
                    if (p.trafficTotal > 0) {
                        Spacer(Modifier.height(4.dp))
                        Text(
                            text = stringResource(R.string.settings_traffic, humanBytes(p.trafficUsed), humanBytes(p.trafficTotal)),
                            style = MaterialTheme.typography.bodySmall,
                            color = TenebraPalette.dim,
                        )
                    }
                    Spacer(Modifier.height(12.dp))
                    OutlinedButton(
                        onClick = viewModel::refreshSubscription,
                        enabled = !isImporting,
                        shape = RectangleShape,
                        modifier = Modifier.fillMaxWidth(),
                    ) {
                        Text(
                            text = stringResource(R.string.settings_refresh),
                            style = MaterialTheme.typography.labelLarge,
                            color = TenebraPalette.text,
                        )
                    }
                    importError?.let {
                        Spacer(Modifier.height(8.dp))
                        Text(it, style = MaterialTheme.typography.bodySmall, color = TenebraPalette.signal)
                    }
                    Spacer(Modifier.height(8.dp))
                    OutlinedButton(
                        onClick = {
                            viewModel.removeProfile()
                            onBack()
                        },
                        shape = RectangleShape,
                        modifier = Modifier.fillMaxWidth(),
                    ) {
                        Text(
                            text = stringResource(R.string.settings_remove),
                            style = MaterialTheme.typography.labelLarge,
                            color = TenebraPalette.signal,
                        )
                    }
                }
            }
        }

        Section(stringResource(R.string.settings_about).uppercase()) {
            Column(Modifier.fillMaxWidth().padding(16.dp)) {
                AboutRow(stringResource(R.string.settings_app), viewModel.appVersion())
                AboutRow(stringResource(R.string.settings_core), viewModel.coreVersion())
                AboutRow(stringResource(R.string.settings_engine), viewModel.engineVersion())
                Spacer(Modifier.height(12.dp))
                Text(
                    text = stringResource(R.string.settings_motto),
                    style = MaterialTheme.typography.bodySmall,
                    color = TenebraPalette.dim,
                )
            }
        }
        Spacer(Modifier.height(24.dp))
    }
}

@Composable
private fun Section(label: String, content: @Composable () -> Unit) {
    Text(
        text = label,
        style = MaterialTheme.typography.labelSmall,
        color = TenebraPalette.dim,
        modifier = Modifier.padding(start = 18.dp, end = 18.dp, top = 18.dp, bottom = 8.dp),
    )
    Box(
        Modifier
            .padding(horizontal = 18.dp)
            .fillMaxWidth()
            .border(1.dp, TenebraPalette.border)
            .background(TenebraPalette.surface),
    ) {
        content()
    }
}

@Composable
private fun AboutRow(label: String, value: String) {
    Row(Modifier.fillMaxWidth().padding(vertical = 3.dp)) {
        Text(
            text = label,
            style = MaterialTheme.typography.bodySmall,
            color = TenebraPalette.dim,
            modifier = Modifier.weight(1f),
        )
        Text(
            text = value,
            style = MaterialTheme.typography.bodySmall,
            color = TenebraPalette.text,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
        )
    }
}

// Bytes -> a short human string for the traffic line. Coarse on purpose (GB with one
// decimal, else whole MB) — this is a glance value, not accounting.
private fun humanBytes(bytes: Long): String {
    if (bytes <= 0) return "0"
    val gb = bytes / (1024.0 * 1024.0 * 1024.0)
    if (gb >= 1.0) return String.format(Locale.US, "%.1f ГБ", gb)
    val mb = bytes / (1024.0 * 1024.0)
    return String.format(Locale.US, "%.0f МБ", mb)
}
