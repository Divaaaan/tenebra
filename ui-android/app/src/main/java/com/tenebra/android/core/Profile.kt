package com.tenebra.android.core

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

// Display-side view of a profile. This intentionally decodes only the fields the UI
// shows; the authoritative profile is the RAW JSON string the core bridge returns
// from importSubscription, which the app stores verbatim and feeds back into
// generateConfig / orderNodes unchanged (see ConfigGenerator). Re-modelling every
// node field here and re-encoding it would risk dropping something the core needs
// (nested tls/reality/transport, the derived `insecure` flag, the `extra` map) and
// could shift the content-stable server IDs across a round-trip. So this is a
// read-only projection, decoded with ignoreUnknownKeys, never re-serialised for the
// core. Shape mirrors core/profile/profile.go (Profile) and core/model/model.go.
@Serializable
data class TenebraProfile(
    val id: String = "",
    val name: String = "",
    val source: String = "",
    val url: String = "",
    @SerialName("nodes") val nodes: List<TenebraNode> = emptyList(),
    val updatedAt: String = "",
    val expiresAt: String? = null,
    val trafficUsed: Long = 0,
    val trafficTotal: Long = 0,
    val managed: Boolean = false,
    val tier: String = "",
)

// A single proxy node, projection of profile.Server (embedded model.Node). Only the
// list/badge fields are decoded. `id` is the stable per-server identifier used for
// selection, last-good tracking and latency keys.
@Serializable
data class TenebraNode(
    val id: String = "",
    val protocol: String = "",
    val name: String = "",
    val server: String = "",
    val port: Int = 0,
    // Surfaced flat by the core when a node disables certificate verification, so the
    // UI can warn without walking the nested tls object. Absent (false) on secure
    // nodes.
    val insecure: Boolean = false,
)
