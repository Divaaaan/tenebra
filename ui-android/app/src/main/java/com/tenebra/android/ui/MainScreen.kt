package com.tenebra.android.ui

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.foundation.text.KeyboardOptions
import com.tenebra.android.R
import com.tenebra.android.bg.TunnelState
import com.tenebra.android.core.TenebraNode
import com.tenebra.android.ui.theme.TenebraPalette

// The whole client surface, laid out for one-handed use (canon "layout B"): status up
// top, the node list fills the middle, and the connect/disconnect control is docked at
// the bottom in the thumb zone. Connect/disconnect are hoisted to the activity because
// the VpnService consent dialog needs an Activity; everything else is view-model driven.
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

    val pings by viewModel.pings.collectAsState()
    val autoMode by viewModel.autoMode.collectAsState()

    val nodes = profile?.nodes ?: emptyList()
    val hasProfile = profile != null
    val active = status == TunnelState.Status.Started || status == TunnelState.Status.Starting

    // Probe latency and load the node->tag map once the node set is known. Re-runs when
    // the node list changes (a fresh import), not on every recomposition.
    LaunchedEffect(nodes.map { it.id }) {
        if (nodes.isNotEmpty()) {
            viewModel.refreshTags()
            viewModel.refreshPings()
        }
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .background(TenebraPalette.ground),
    ) {
        TopBar(onOpenLogs = onOpenLogs)

        StatusPanel(
            status = status,
            currentNode = nodes.firstOrNull { it.id == selectedId },
            hasProfile = hasProfile,
            connected = status == TunnelState.Status.Started,
        )
        tunnelError?.let {
            MessageText(text = it, error = true, modifier = Modifier.padding(horizontal = 18.dp, vertical = 6.dp))
        }

        if (!hasProfile) {
            EmptyState(
                isImporting = isImporting,
                importError = importError,
                onImport = viewModel::importSubscription,
                modifier = Modifier.weight(1f),
            )
        } else {
            SectionLabel(stringResource(R.string.nodes))
            // The node AUTO currently resolves to, for its subtitle — computed here so it
            // tracks ping changes without the view model exposing derived state.
            val fastestName = remember(pings, nodes) {
                val best = pings.filterValues { it > 0 }.minByOrNull { it.value }?.key
                    ?: nodes.firstOrNull()?.id
                nodes.firstOrNull { it.id == best }?.let { it.name.ifBlank { it.server } }
            }
            LazyColumn(
                modifier = Modifier
                    .weight(1f)
                    .fillMaxWidth(),
                contentPadding = androidx.compose.foundation.layout.PaddingValues(horizontal = 18.dp),
                verticalArrangement = Arrangement.spacedBy(6.dp),
            ) {
                item(key = "__auto__") {
                    AutoRow(
                        selected = autoMode,
                        fastestName = fastestName,
                        onClick = { viewModel.selectAuto() },
                    )
                }
                items(nodes, key = { it.id }) { node ->
                    NodeRow(
                        node = node,
                        // In AUTO no specific node reads as chosen — the AUTO row does.
                        selected = !autoMode && node.id == selectedId,
                        ping = pings[node.id],
                        onClick = { viewModel.selectNode(node.id) },
                    )
                }
            }
            importError?.let {
                MessageText(text = it, error = true, modifier = Modifier.padding(horizontal = 18.dp, vertical = 6.dp))
            }
            DockButton(
                active = active,
                busy = status == TunnelState.Status.Starting || status == TunnelState.Status.Stopping,
                enabled = active || hasProfile,
                onConnect = onConnect,
                onDisconnect = onDisconnect,
            )
        }
    }
}

@Composable
private fun TopBar(onOpenLogs: () -> Unit) {
    Row(
        verticalAlignment = Alignment.CenterVertically,
        modifier = Modifier
            .fillMaxWidth()
            .padding(start = 18.dp, end = 8.dp, top = 14.dp, bottom = 6.dp),
    ) {
        Text(
            text = "TENEBRA",
            style = MaterialTheme.typography.titleLarge,
            color = TenebraPalette.signal,
            modifier = Modifier.weight(1f),
        )
        TextButton(onClick = onOpenLogs) {
            Text(
                text = stringResource(R.string.diagnostics),
                style = MaterialTheme.typography.labelMedium,
                color = TenebraPalette.dim,
            )
        }
    }
}

@Composable
private fun StatusPanel(
    status: TunnelState.Status,
    currentNode: TenebraNode?,
    hasProfile: Boolean,
    connected: Boolean,
) {
    Column(
        modifier = Modifier
            .padding(horizontal = 18.dp)
            .fillMaxWidth()
            .border(1.dp, TenebraPalette.border)
            .background(TenebraPalette.surface)
            .padding(16.dp),
    ) {
        EclipseGlyph(on = connected)
        Spacer(Modifier.height(10.dp))
        Text(
            text = stringResource(statusLabel(status)),
            style = MaterialTheme.typography.displaySmall,
            color = when (status) {
                TunnelState.Status.Started -> TenebraPalette.wordOn
                TunnelState.Status.Starting, TunnelState.Status.Stopping -> TenebraPalette.wordPending
                TunnelState.Status.Stopped -> TenebraPalette.wordOff
            },
        )
        val subtitle = when {
            connected && currentNode != null -> stringResource(R.string.current_via, currentNode.name.ifBlank { currentNode.server })
            !hasProfile -> stringResource(R.string.no_subscription)
            else -> null
        }
        subtitle?.let {
            Spacer(Modifier.height(8.dp))
            Text(text = it, style = MaterialTheme.typography.bodySmall, color = TenebraPalette.dim)
        }
    }
}

// The signature mark: a faint full ring (the occulted sun) with a signal crescent of
// light escaping when the tunnel is up, nothing escaping when it is down.
@Composable
private fun EclipseGlyph(on: Boolean) {
    Canvas(Modifier.size(14.dp)) {
        val sw = 1.5.dp.toPx()
        val inset = sw / 2
        drawCircle(
            color = TenebraPalette.border,
            radius = size.minDimension / 2 - inset,
            style = Stroke(sw),
        )
        if (on) {
            drawArc(
                color = TenebraPalette.signal,
                startAngle = -60f,
                sweepAngle = 150f,
                useCenter = false,
                topLeft = Offset(inset, inset),
                size = Size(size.width - sw, size.height - sw),
                style = Stroke(sw, cap = StrokeCap.Round),
            )
        }
    }
}

@Composable
private fun SectionLabel(text: String) {
    Text(
        text = text.uppercase(),
        style = MaterialTheme.typography.labelSmall,
        color = TenebraPalette.dim,
        modifier = Modifier.padding(start = 18.dp, end = 18.dp, top = 18.dp, bottom = 8.dp),
    )
}

@Composable
private fun NodeRow(
    node: TenebraNode,
    selected: Boolean,
    ping: Int?,
    onClick: () -> Unit,
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(if (selected) TenebraPalette.surfaceRaised else TenebraPalette.surface)
            .border(1.dp, TenebraPalette.border)
            .drawBehind {
                if (selected) drawRect(TenebraPalette.signal, size = Size(2.dp.toPx(), size.height))
            }
            .clickable(onClick = onClick)
            .padding(start = 14.dp, end = 12.dp, top = 10.dp, bottom = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f)) {
            Text(
                text = node.name.ifBlank { node.server },
                style = MaterialTheme.typography.bodyMedium,
                color = TenebraPalette.text,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
            Spacer(Modifier.height(2.dp))
            Text(
                text = "${node.protocol} · ${node.server}:${node.port}",
                style = MaterialTheme.typography.bodySmall,
                color = TenebraPalette.dim,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
        }
        if (node.insecure) {
            Spacer(Modifier.width(12.dp))
            Text(
                text = "!",
                style = MaterialTheme.typography.headlineSmall,
                color = TenebraPalette.signal,
            )
        }
        ping?.let {
            Spacer(Modifier.width(12.dp))
            PingBadge(it)
        }
    }
}

// The AUTO pseudo-node: sits at the top of the list and, when chosen, keeps the tunnel
// on the lowest-ping node. Its subtitle names the node it currently resolves to.
@Composable
private fun AutoRow(
    selected: Boolean,
    fastestName: String?,
    onClick: () -> Unit,
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(if (selected) TenebraPalette.surfaceRaised else TenebraPalette.surface)
            .border(1.dp, TenebraPalette.border)
            .drawBehind {
                if (selected) drawRect(TenebraPalette.signal, size = Size(2.dp.toPx(), size.height))
            }
            .clickable(onClick = onClick)
            .padding(start = 14.dp, end = 12.dp, top = 10.dp, bottom = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f)) {
            Text(
                text = "◈ ${stringResource(R.string.node_auto)}",
                style = MaterialTheme.typography.bodyMedium,
                color = TenebraPalette.text,
                maxLines = 1,
            )
            Spacer(Modifier.height(2.dp))
            Text(
                text = fastestName?.let { stringResource(R.string.node_auto_current, it) }
                    ?: stringResource(R.string.node_auto_measuring),
                style = MaterialTheme.typography.bodySmall,
                color = TenebraPalette.dim,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
        }
        if (selected) {
            Spacer(Modifier.width(12.dp))
            Text(
                text = stringResource(R.string.node_auto_on),
                style = MaterialTheme.typography.bodySmall,
                color = TenebraPalette.good,
            )
        }
    }
}

// Latency chip: colour by the desktop ping scale (good/warn/signal) or dim for no
// answer. A null ping renders nothing — the sweep hasn't reported this node yet.
@Composable
private fun PingBadge(ms: Int) {
    Text(
        text = if (ms < 0) stringResource(R.string.ping_no_answer) else stringResource(R.string.ping_ms, ms),
        style = MaterialTheme.typography.bodySmall,
        color = TenebraPalette.ping(ms),
    )
}

@Composable
private fun DockButton(
    active: Boolean,
    busy: Boolean,
    enabled: Boolean,
    onConnect: () -> Unit,
    onDisconnect: () -> Unit,
) {
    Box(
        modifier = Modifier
            .fillMaxWidth()
            .background(TenebraPalette.ground)
            .padding(start = 18.dp, end = 18.dp, top = 12.dp, bottom = 18.dp),
    ) {
        Button(
            onClick = { if (active) onDisconnect() else onConnect() },
            enabled = enabled && !busy,
            shape = RectangleShape,
            colors = ButtonDefaults.buttonColors(
                containerColor = if (active) TenebraPalette.surface else TenebraPalette.signal,
                contentColor = if (active) TenebraPalette.text else TenebraPalette.ground,
                disabledContainerColor = TenebraPalette.surface,
                disabledContentColor = TenebraPalette.dim,
            ),
            modifier = Modifier
                .fillMaxWidth()
                .height(52.dp)
                .then(if (active) Modifier.border(1.dp, TenebraPalette.border) else Modifier),
        ) {
            if (busy) {
                CircularProgressIndicator(
                    modifier = Modifier.size(18.dp),
                    strokeWidth = 2.dp,
                    color = TenebraPalette.text,
                )
                Spacer(Modifier.width(8.dp))
            }
            Text(
                text = stringResource(if (active) R.string.disconnect else R.string.connect),
                style = MaterialTheme.typography.labelLarge,
            )
        }
    }
}

@Composable
private fun EmptyState(
    isImporting: Boolean,
    importError: String?,
    onImport: (String) -> Unit,
    modifier: Modifier = Modifier,
) {
    var url by rememberSaveable { mutableStateOf("") }
    Column(
        modifier = modifier
            .fillMaxWidth()
            .padding(horizontal = 18.dp),
        verticalArrangement = Arrangement.Center,
    ) {
        SectionLabelInline(stringResource(R.string.empty_start_label))
        Spacer(Modifier.height(8.dp))
        OutlinedTextField(
            value = url,
            onValueChange = { url = it },
            singleLine = true,
            placeholder = { Text(stringResource(R.string.subscription_url_hint), color = TenebraPalette.dim) },
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
            shape = RectangleShape,
            modifier = Modifier.fillMaxWidth(),
        )
        Spacer(Modifier.height(10.dp))
        Button(
            onClick = { onImport(url) },
            enabled = url.isNotBlank() && !isImporting,
            shape = RectangleShape,
            colors = ButtonDefaults.buttonColors(
                containerColor = TenebraPalette.signal,
                contentColor = TenebraPalette.ground,
                disabledContainerColor = TenebraPalette.surface,
                disabledContentColor = TenebraPalette.dim,
            ),
            modifier = Modifier
                .fillMaxWidth()
                .height(52.dp),
        ) {
            if (isImporting) {
                CircularProgressIndicator(
                    modifier = Modifier.size(18.dp),
                    strokeWidth = 2.dp,
                    color = TenebraPalette.ground,
                )
                Spacer(Modifier.width(8.dp))
            }
            Text(stringResource(R.string.import_action), style = MaterialTheme.typography.labelLarge)
        }
        importError?.let {
            Spacer(Modifier.height(10.dp))
            MessageText(text = it, error = true)
        }
        Spacer(Modifier.height(14.dp))
        Text(
            text = stringResource(R.string.empty_hint),
            style = MaterialTheme.typography.bodySmall,
            color = TenebraPalette.dim,
        )
    }
}

@Composable
private fun SectionLabelInline(text: String) {
    Text(
        text = text.uppercase(),
        style = MaterialTheme.typography.labelSmall,
        color = TenebraPalette.dim,
    )
}

@Composable
private fun MessageText(text: String, error: Boolean, modifier: Modifier = Modifier) {
    Text(
        text = text,
        style = MaterialTheme.typography.bodySmall,
        color = if (error) TenebraPalette.signal else TenebraPalette.dim,
        modifier = modifier,
    )
}

private fun statusLabel(status: TunnelState.Status): Int = when (status) {
    TunnelState.Status.Stopped -> R.string.status_disconnected
    TunnelState.Status.Starting -> R.string.status_connecting
    TunnelState.Status.Started -> R.string.status_connected
    TunnelState.Status.Stopping -> R.string.status_stopping
}

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
        title = { Text(stringResource(R.string.crash_title)) },
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
                Text(stringResource(R.string.crash_copy))
            }
        },
        dismissButton = {
            TextButton(onClick = {
                onClear()
                open = false
            }) {
                Text(stringResource(R.string.crash_clear))
            }
        },
    )
}
