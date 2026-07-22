package com.tenebra.android.ui

import android.content.Intent
import android.widget.Toast
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import com.tenebra.android.R
import com.tenebra.android.bg.DiagnosticsReport
import com.tenebra.android.bg.LogStore

// The Diagnostics screen. It shows the captured engine + app log, the last crash and a
// short device header, and lets the tester get all of it off the phone with one tap —
// Copy or Share — without adb. Copy/Share run the text through the scrubber first
// (MainViewModel.buildDiagnosticsReport); nothing here ever touches the network.
@Composable
fun LogsScreen(
    viewModel: MainViewModel,
    onBack: () -> Unit,
) {
    BackHandler(onBack = onBack)

    val context = LocalContext.current
    val clipboard = LocalClipboardManager.current

    val lines by viewModel.logLines.collectAsState()
    val lastCrash by viewModel.lastCrash.collectAsState()
    // The header is stable for the session (versions/device), so build it once.
    val header = remember { DiagnosticsReport.header() }

    val listState = rememberLazyListState()
    // Stick to the newest line only when the user is already at the bottom, so an
    // arriving line never yanks the view while they are scrolled up reading history.
    val atBottom by remember {
        derivedStateOf {
            val last = listState.layoutInfo.visibleItemsInfo.lastOrNull()
            last == null || last.index >= lines.size - 1
        }
    }
    // Key on the snapshot itself, not its size: at the ring buffer's cap the size stops
    // changing while lines keep arriving, and we still want to follow the newest one.
    LaunchedEffect(lines) {
        if (atBottom && lines.isNotEmpty()) listState.scrollToItem(lines.size - 1)
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
                TextButton(onClick = onBack) {
                    Text(stringResource(R.string.diagnostics_back))
                }
                Text(
                    text = stringResource(R.string.diagnostics_title).uppercase(),
                    style = MaterialTheme.typography.titleLarge,
                    color = MaterialTheme.colorScheme.primary,
                    modifier = Modifier.weight(1f).padding(start = 8.dp),
                )
            }

            Spacer(Modifier.height(12.dp))

            Text(
                text = header.trimEnd(),
                style = MaterialTheme.typography.bodySmall,
                fontFamily = FontFamily.Monospace,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )

            Spacer(Modifier.height(12.dp))

            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Button(
                    onClick = { shareReport(context, viewModel.buildDiagnosticsReport()) },
                    shape = RectangleShape,
                    modifier = Modifier.weight(1f),
                ) {
                    Text(stringResource(R.string.diagnostics_share))
                }
                OutlinedButton(
                    onClick = {
                        clipboard.setText(AnnotatedString(viewModel.buildDiagnosticsReport()))
                        Toast.makeText(context, R.string.diagnostics_copied, Toast.LENGTH_SHORT).show()
                    },
                    shape = RectangleShape,
                    modifier = Modifier.weight(1f),
                ) {
                    Text(stringResource(R.string.diagnostics_copy))
                }
                OutlinedButton(
                    onClick = viewModel::clearDiagnostics,
                    shape = RectangleShape,
                    modifier = Modifier.weight(1f),
                ) {
                    Text(stringResource(R.string.diagnostics_clear))
                }
            }

            Spacer(Modifier.height(8.dp))

            Text(
                text = stringResource(R.string.diagnostics_privacy),
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )

            lastCrash?.let { crash ->
                Spacer(Modifier.height(16.dp))
                Text(
                    text = stringResource(R.string.diagnostics_last_crash).uppercase(),
                    style = MaterialTheme.typography.titleMedium,
                    color = MaterialTheme.colorScheme.error,
                )
                Spacer(Modifier.height(4.dp))
                Text(
                    text = crash.trim(),
                    style = MaterialTheme.typography.bodySmall,
                    fontFamily = FontFamily.Monospace,
                    color = MaterialTheme.colorScheme.onSurface,
                    modifier = Modifier
                        .fillMaxWidth()
                        .heightIn(max = 200.dp)
                        .verticalScroll(rememberScrollState()),
                )
            }

            Spacer(Modifier.height(16.dp))

            Text(
                text = "${stringResource(R.string.diagnostics_log).uppercase()} (${lines.size})",
                style = MaterialTheme.typography.titleMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Spacer(Modifier.height(4.dp))

            if (lines.isEmpty()) {
                Text(
                    text = stringResource(R.string.diagnostics_empty),
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            } else {
                LazyColumn(
                    state = listState,
                    // weight so the log takes the remaining height and scrolls within it.
                    modifier = Modifier.weight(1f).fillMaxWidth(),
                    verticalArrangement = Arrangement.spacedBy(2.dp),
                ) {
                    items(lines) { line -> LogRow(line) }
                }
            }
        }
    }
}

@Composable
private fun LogRow(line: LogStore.Line) {
    Text(
        text = DiagnosticsReport.formatLine(line),
        style = MaterialTheme.typography.bodySmall,
        fontFamily = FontFamily.Monospace,
        color = when (line.level) {
            LogStore.Level.Error -> MaterialTheme.colorScheme.error
            LogStore.Level.Warn -> MaterialTheme.colorScheme.primary
            else -> MaterialTheme.colorScheme.onSurfaceVariant
        },
    )
}

// Hand the already-scrubbed report to any app the user picks (Telegram, mail, notes).
// text/plain keeps it universal; the chooser is what makes it "one tap to send".
private fun shareReport(context: android.content.Context, text: String) {
    val send = Intent(Intent.ACTION_SEND).apply {
        type = "text/plain"
        putExtra(Intent.EXTRA_SUBJECT, "Tenebra diagnostics")
        putExtra(Intent.EXTRA_TEXT, text)
    }
    context.startActivity(Intent.createChooser(send, null))
}
