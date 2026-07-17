package com.tenebra.android.ui.theme

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.sp

// Brutalist, single-look theme: near-black ground, signal-orange accent, monospace
// display. Committed to dark on purpose (the brand is dark), so it ignores the system
// light/dark setting rather than offering a washed-out light variant.

private val Accent = Color(0xFFFF3D00)
private val Ground = Color(0xFF0E0E0E)
private val Surface = Color(0xFF161616)
private val SurfaceVariant = Color(0xFF1E1E1E)
private val OnGround = Color(0xFFF5F5F5)
private val Muted = Color(0xFF8A8A8A)
private val Danger = Color(0xFFFF5449)

private val TenebraColors = darkColorScheme(
    primary = Accent,
    onPrimary = Ground,
    secondary = Accent,
    onSecondary = Ground,
    background = Ground,
    onBackground = OnGround,
    surface = Surface,
    onSurface = OnGround,
    surfaceVariant = SurfaceVariant,
    onSurfaceVariant = Muted,
    outline = Muted,
    error = Danger,
    onError = Ground,
)

private val TenebraTypography = Typography().run {
    val mono = FontFamily.Monospace
    copy(
        headlineMedium = headlineMedium.copy(fontFamily = mono, fontWeight = FontWeight.Bold),
        titleLarge = titleLarge.copy(fontFamily = mono, fontWeight = FontWeight.Bold),
        titleMedium = titleMedium.copy(fontFamily = mono),
        labelLarge = labelLarge.copy(fontFamily = mono, fontWeight = FontWeight.Bold),
        bodyMedium = TextStyle(fontFamily = mono, fontSize = 14.sp),
        bodySmall = bodySmall.copy(fontFamily = mono),
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
