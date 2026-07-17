// Root build file. Plugin versions are declared once here (apply false) and applied
// per-module, which is the current AGP convention. All versions live in the version
// catalog at gradle/libs.versions.toml.
plugins {
    alias(libs.plugins.android.application) apply false
    alias(libs.plugins.kotlin.android) apply false
    alias(libs.plugins.kotlin.compose) apply false
    alias(libs.plugins.kotlin.serialization) apply false
}
