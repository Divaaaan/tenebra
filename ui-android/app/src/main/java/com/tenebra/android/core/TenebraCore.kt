package com.tenebra.android.core

// Single indirection over the gomobile-generated Tenebra core binding, so the exact
// generated symbol is named in ONE place. Our generator (the ../../mobile wrapper,
// package `tenebracore`) is bound together with libbox into the one tenebra.aar by
//   gomobile bind -target android -androidapi 23 -javapkg io.nekohasekai \
//     -o ui-android/app/libs/tenebra.aar . <libbox>   (scripts/build-libbox-android.sh)
// so gomobile emits the class `io.nekohasekai.tenebracore.Tenebracore` with static
// methods; each Go `(string, error)` return becomes a String that throws on error.
// (libbox keeps io.nekohasekai.libbox.* from the same -javapkg, so bg/ is unchanged.)
//
// VERIFY the class/method spelling against the generated tenebra.aar after the first
// Android bind (the iOS side leaves the identical note in TunnelManager.swift). If
// gomobile spells the sub-package differently, fix it here only — nothing else
// references the raw symbol.
internal object TenebraCore {

    fun version(): String =
        io.nekohasekai.tenebracore.Tenebracore.version()

    @Throws(Exception::class)
    fun generateConfig(requestJson: String): String =
        io.nekohasekai.tenebracore.Tenebracore.generateConfig(requestJson)

    @Throws(Exception::class)
    fun importSubscription(requestJson: String): String =
        io.nekohasekai.tenebracore.Tenebracore.importSubscription(requestJson)

    @Throws(Exception::class)
    fun orderNodes(requestJson: String): String =
        io.nekohasekai.tenebracore.Tenebracore.orderNodes(requestJson)
}
