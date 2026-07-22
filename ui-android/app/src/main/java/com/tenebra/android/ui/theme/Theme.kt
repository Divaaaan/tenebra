package com.tenebra.android.ui.theme

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.Font
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.em
import androidx.compose.ui.unit.sp
import com.tenebra.android.R

// Brutalist, single-look theme shared with the desktop client: near-black ground, a
// single rationed signal-orange, monospace everything. Committed to dark on purpose
// (the brand is dark), so it ignores the system light/dark setting rather than
// offering a washed-out light variant. The type is one family — JetBrains Mono, whose
// weights and tracking carry the whole hierarchy — because the brand is literally
// "brutalist mono" and JBM covers Cyrillic (the status words are Russian).

// ---- Canon tokens (mirror ui-desktop/src/styles/tokens.css) --------------------
private val Signal = Color(0xFFFF3D00) // the only accent; rationed
private val Ground = Color(0xFF0E0E0E) // near-black, never pure #000
private val Surface1 = Color(0xFF151515)
private val Surface2 = Color(0xFF1B1B1B)
private val Border = Color(0xFF262626)
private val Text = Color(0xFFE6E6E2)
private val Dim = Color(0xFF7D7D78)
private val Good = Color(0xFF46C07A) // ONLY for connected/protected
private val Warn = Color(0xFFD9A441)

// Semantic colors Material's scheme has no slot for. Screens read these directly.
object TenebraPalette {
    val signal = Signal
    val ground = Ground
    val surface = Surface1
    val surfaceRaised = Surface2
    val border = Border
    val text = Text
    val dim = Dim
    val good = Good
    val warn = Warn

    // Status-word tints: the big word reads harsh in pure good/text, so it is mixed.
    // Error stays the hard signal — that one is meant to alarm.
    val wordOn = Color(0xFF8FD3A6) // good 58% + text 42%, precomputed
    val wordOff = Color(0xFFB0B0AB) // text 50% + dim 50%, precomputed
    val wordPending = Warn

    // Ping-scale buckets for the node latency badges. A negative value is "no answer".
    fun ping(ms: Int): Color = when {
        ms < 0 -> dim
        ms < 80 -> good
        ms < 200 -> warn
        else -> signal
    }
}

private val TenebraColors = darkColorScheme(
    primary = Signal,
    onPrimary = Ground,
    secondary = Signal,
    onSecondary = Ground,
    background = Ground,
    onBackground = Text,
    surface = Surface1,
    onSurface = Text,
    surfaceVariant = Surface2,
    onSurfaceVariant = Dim,
    outline = Border,
    error = Signal,
    onError = Ground,
)

val JetBrainsMono = FontFamily(
    Font(R.font.jetbrains_mono_regular, FontWeight.Normal),
    Font(R.font.jetbrains_mono_semibold, FontWeight.SemiBold),
    Font(R.font.jetbrains_mono_extrabold, FontWeight.ExtraBold),
)

// Canon type scale (px->sp) and tracking (em), mapped onto Material's slots so existing
// call sites keep working while the values become the brutalist scale.
private val TenebraTypography = Typography().run {
    val f = JetBrainsMono
    copy(
        // the 34px hero status word
        displaySmall = TextStyle(fontFamily = f, fontWeight = FontWeight.ExtraBold, fontSize = 34.sp, letterSpacing = (-0.03).em),
        // stat / KPI values
        headlineSmall = TextStyle(fontFamily = f, fontWeight = FontWeight.ExtraBold, fontSize = 22.sp, letterSpacing = (-0.02).em),
        // TENEBRA wordmark
        titleLarge = TextStyle(fontFamily = f, fontWeight = FontWeight.ExtraBold, fontSize = 18.sp, letterSpacing = 0.02.em),
        // node code / current-node line
        titleMedium = TextStyle(fontFamily = f, fontWeight = FontWeight.SemiBold, fontSize = 15.sp, letterSpacing = 0.01.em),
        // node row primary
        bodyMedium = TextStyle(fontFamily = f, fontWeight = FontWeight.Normal, fontSize = 13.sp, letterSpacing = 0.02.em),
        // dense data / meta
        bodySmall = TextStyle(fontFamily = f, fontWeight = FontWeight.Normal, fontSize = 11.sp, letterSpacing = 0.04.em),
        // button text
        labelLarge = TextStyle(fontFamily = f, fontWeight = FontWeight.SemiBold, fontSize = 13.sp, letterSpacing = 0.14.em),
        // chips / meta
        labelMedium = TextStyle(fontFamily = f, fontWeight = FontWeight.Normal, fontSize = 11.sp, letterSpacing = 0.12.em),
        // section labels
        labelSmall = TextStyle(fontFamily = f, fontWeight = FontWeight.Normal, fontSize = 10.sp, letterSpacing = 0.18.em),
    )
}

@Composable
fun TenebraTheme(content: @Composable () -> Unit) {
    // isSystemInDarkTheme is read only so the call site is honest about being
    // theme-aware; the scheme is intentionally the same either way.
    @Suppress("UNUSED_VARIABLE")
    val ignored = isSystemInDarkTheme()
    MaterialTheme(
        colorScheme = TenebraColors,
        typography = TenebraTypography,
        content = content,
    )
}
