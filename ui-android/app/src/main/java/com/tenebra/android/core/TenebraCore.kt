package com.tenebra.android.core

// Single indirection over the gomobile-generated Tenebra core binding, so the exact
// generated symbol is named in ONE place. The bind command
//   gomobile bind -target android -androidapi 23 -javapkg com.tenebra.core \
//     -o ui-android/app/libs/tenebra-core.aar ./core-bridge
// emits the class `com.tenebra.core.Tenebracore` with static methods; each Go
// `(string, error)` return becomes a String that throws on error.
//
// VERIFY the class/method spelling against the generated headers after the first
// Android bind (the iOS side leaves the identical note in TunnelManager.swift). If
// gomobile spells anything differently, fix it here only — nothing else references
// the raw symbol.
internal object TenebraCore {

    fun version(): String =
        com.tenebra.core.Tenebracore.version()

    @Throws(Exception::class)
    fun generateConfig(requestJson: String): String =
        com.tenebra.core.Tenebracore.generateConfig(requestJson)

    @Throws(Exception::class)
    fun importSubscription(requestJson: String): String =
        com.tenebra.core.Tenebracore.importSubscription(requestJson)

    @Throws(Exception::class)
    fun orderNodes(requestJson: String): String =
        com.tenebra.core.Tenebracore.orderNodes(requestJson)
}
