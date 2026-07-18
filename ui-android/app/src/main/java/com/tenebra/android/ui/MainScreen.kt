package com.tenebra.android.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.foundation.text.KeyboardOptions
import com.tenebra.android.R
import com.tenebra.android.bg.TunnelState
import com.tenebra.android.core.TenebraNode

// The whole client surface: status + connect toggle, subscription import, node list.
// Connect/disconnect are hoisted to the activity because the VpnService consent
// dialog needs an Activity; everything else is driven by the view model.
@Composable
fun MainScreen(
    viewModel: MainViewModel,
    onConnect: () -> Unit,
    onDisconnect: () -> Unit,
    onOpenLogs: () -> Unit,
) {
    val profile by viewModel.profile.collectAsState()
    val status by viewModel.status.collectAsState()
    val selectedId by viewModel.selectedServerId.collectAsState()
    val isImporting by viewModel.isImporting.collectAsState()
    val importError by viewModel.importError.collectAsState()
    val tunnelError by viewModel.tunnelError.collectAsState()
    val lastCrash by viewModel.lastCrash.collectAsState()

    lastCrash?.let { crash ->
        CrashDialog(text = crash, onClear = viewModel::clearCrashLog)
    }

    Scaffold { padding ->
        Column(
            modifier = Modifier
                .padding(padding)
                .fillMaxSize()
                .padding(16.dp),
        ) {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                modifier = Modifier.fillMaxWidth(),
            ) {
                Text(
                    text = "TENEBRA",
                    style = MaterialTheme.typography.headlineMedium,
                    color = MaterialTheme.colorScheme.primary,
                    modifier = Modifier.weight(1f),
                )
                // Unobtrusive way into the log/diagnostics screen; does not touch the
                // connect flow.
                TextButton(onClick = onOpenLogs) {
                    Text(androidx.compose.ui.res.stringResource(R.string.diagnostics))
                }
            }
            Spacer(Modifier.height(16.dp))

            StatusCard(
                status = status,
                hasProfile = profile != null,
                onConnect = onConnect,
                onDisconnect = onDisconnect,
            )

            tunnelError?.let {
                Spacer(Modifier.height(8.dp))
                MessageText(text = it, error = true)
            }

            Spacer(Modifier.height(24.dp))

            ImportRow(
                isImporting = isImporting,
                onImport = viewModel::importSubscription,
            )
            importError?.let {
                Spacer(Modifier.height(8.dp))
                MessageText(text = it, error = true)
            }

            Spacer(Modifier.height(24.dp))

            Text(
                text = stringResourceUpper(R.string.nodes),
                style = MaterialTheme.typography.titleMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Spacer(Modifier.height(8.dp))

            val nodes = profile?.nodes ?: emptyList()
            if (nodes.isEmpty()) {
                MessageText(text = androidx.compose.ui.res.stringResource(R.string.no_nodes), error = false)
            } else {
                LazyColumn(
                    // weight so the list takes the remaining height and scrolls within
                    // it, rather than being measured against the full column height.
                    modifier = Modifier.weight(1f),
                    verticalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    items(nodes, key = { it.id }) { node ->
                        NodeRow(
                            node = node,
                            selected = node.id == selectedId,
                            onClick = { viewModel.selectNode(node.id) },
                        )
                    }
                }
            }
        }
    }
}

@Composable
private fun StatusCard(
    status: TunnelState.Status,
    hasProfile: Boolean,
    onConnect: () -> Unit,
    onDisconnect: () -> Unit,
) {
    Card(
        shape = RectangleShape,
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surface),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Column(Modifier.padding(16.dp)) {
            Text(
                text = androidx.compose.ui.res.stringResource(statusLabel(status)),
                style = MaterialTheme.typography.titleLarge,
                color = when (status) {
                    TunnelState.Status.Started -> MaterialTheme.colorScheme.primary
                    else -> MaterialTheme.colorScheme.onSurface
                },
            )
            Spacer(Modifier.height(12.dp))

            val active = status == TunnelState.Status.Started ||
                status == TunnelState.Status.Starting
            val busy = status == TunnelState.Status.Starting ||
                status == TunnelState.Status.Stopping

            Button(
                onClick = { if (active) onDisconnect() else onConnect() },
                enabled = (active || hasProfile) && !busy,
                shape = RectangleShape,
                colors = ButtonDefaults.buttonColors(
                    containerColor = if (active) {
                        MaterialTheme.colorScheme.surfaceVariant
                    } else {
                        MaterialTheme.colorScheme.primary
                    },
                    contentColor = if (active) {
                        MaterialTheme.colorScheme.onSurface
                    } else {
                        MaterialTheme.colorScheme.onPrimary
                    },
                ),
                modifier = Modifier.fillMaxWidth(),
            ) {
                if (busy) {
                    CircularProgressIndicator(
                        modifier = Modifier.height(18.dp).width(18.dp),
                        strokeWidth = 2.dp,
                        color = MaterialTheme.colorScheme.onSurface,
                    )
                    Spacer(Modifier.width(8.dp))
                }
                Text(
                    text = androidx.compose.ui.res.stringResource(
                        if (active) R.string.disconnect else R.string.connect,
                    ),
                )
            }
        }
    }
}

@Composable
private fun ImportRow(
    isImporting: Boolean,
    onImport: (String) -> Unit,
) {
    var url by rememberSaveable { mutableStateOf("") }
    Column {
        OutlinedTextField(
            value = url,
            onValueChange = { url = it },
            singleLine = true,
            label = { Text(androidx.compose.ui.res.stringResource(R.string.import_subscription)) },
            placeholder = { Text(androidx.compose.ui.res.stringResource(R.string.subscription_url_hint)) },
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
            shape = RectangleShape,
            modifier = Modifier.fillMaxWidth(),
        )
        Spacer(Modifier.height(8.dp))
        OutlinedButton(
            onClick = { onImport(url) },
            enabled = url.isNotBlank() && !isImporting,
            shape = RectangleShape,
            modifier = Modifier.fillMaxWidth(),
        ) {
            if (isImporting) {
                CircularProgressIndicator(
                    modifier = Modifier.height(18.dp).width(18.dp),
                    strokeWidth = 2.dp,
                    color = MaterialTheme.colorScheme.primary,
                )
                Spacer(Modifier.width(8.dp))
            }
            Text(androidx.compose.ui.res.stringResource(R.string.import_action))
        }
    }
}

@Composable
private fun NodeRow(
    node: TenebraNode,
    selected: Boolean,
    onClick: () -> Unit,
) {
    Card(
        shape = RectangleShape,
        colors = CardDefaults.cardColors(
            containerColor = if (selected) {
                MaterialTheme.colorScheme.surfaceVariant
            } else {
                MaterialTheme.colorScheme.surface
            },
        ),
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onClick),
    ) {
        Row(
            modifier = Modifier.padding(12.dp).fillMaxWidth(),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Column(Modifier.weight(1f)) {
                Text(
                    text = node.name.ifBlank { node.server },
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurface,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                Text(
                    text = "${node.protocol} · ${node.server}:${node.port}",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
            if (node.insecure) {
                Text(
                    text = "!",
                    style = MaterialTheme.typography.titleLarge,
                    color = MaterialTheme.colorScheme.error,
                )
                Spacer(Modifier.width(12.dp))
            }
            if (selected) {
                Text(
                    text = "✓", // check mark
                    style = MaterialTheme.typography.titleLarge,
                    color = MaterialTheme.colorScheme.primary,
                )
            }
        }
    }
}

@Composable
private fun MessageText(text: String, error: Boolean) {
    Text(
        text = text,
        style = MaterialTheme.typography.bodySmall,
        color = if (error) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.onSurfaceVariant,
    )
}

private fun statusLabel(status: TunnelState.Status): Int = when (status) {
    TunnelState.Status.Stopped -> R.string.status_disconnected
    TunnelState.Status.Starting -> R.string.status_connecting
    TunnelState.Status.Started -> R.string.status_connected
    TunnelState.Status.Stopping -> R.string.status_stopping
}

@Composable
private fun stringResourceUpper(id: Int): String =
    androidx.compose.ui.res.stringResource(id).uppercase()

// Shown at launch when the previous run left an uncaught-crash report. Lets the tester
// copy the stack trace out (no adb needed) and clear it. "Clear" deletes the saved
// report; dismissing (back/scrim) only hides it for this launch so it can still be
// copied later.
@Composable
private fun CrashDialog(text: String, onClear: () -> Unit) {
    var open by rememberSaveable { mutableStateOf(true) }
    if (!open) return
    val clipboard = LocalClipboardManager.current
    AlertDialog(
        onDismissRequest = { open = false },
        title = { Text(androidx.compose.ui.res.stringResource(R.string.crash_title)) },
        text = {
            Text(
                text = text,
                style = MaterialTheme.typography.bodySmall,
                modifier = Modifier
                    .heightIn(max = 320.dp)
                    .verticalScroll(rememberScrollState()),
            )
        },
        confirmButton = {
            TextButton(onClick = { clipboard.setText(AnnotatedString(text)) }) {
                Text(androidx.compose.ui.res.stringResource(R.string.crash_copy))
            }
        },
        dismissButton = {
            TextButton(onClick = {
                onClear()
                open = false
            }) {
                Text(androidx.compose.ui.res.stringResource(R.string.crash_clear))
            }
        },
    )
}
