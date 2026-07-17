package com.tenebra.android.core

import kotlinx.serialization.decodeFromString
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonObject

// Thin Kotlin wrapper over the Tenebra core bridge (bundled in tenebra.aar, built by
// CI from the ../../mobile wrapper via `gomobile bind ... -javapkg io.nekohasekai`).
// gomobile renders the wrapper package's exported funcs as static methods on a class
// named after the package: io.nekohasekai.tenebracore.Tenebracore.{generateConfig,
// importSubscription,orderNodes,version}. Each Go `(string, error)` return becomes a
// Kotlin method that returns String and throws on error.
//
// VERIFY the exact class/method spelling against the generated headers after the
// first Android bind (the iOS side leaves the same note, guarded by #if canImport).
// Kotlin has no canImport equivalent, so the reference is direct: it compiles once
// tenebra.aar is present in app/libs, and fails loudly if the ABI drifts — which is
// the intended signal, not something to paper over.
//
// The three call envelopes are the frozen ABI from core-bridge/{generate,import,
// order}.go. The load-bearing invariant on Android is tun.externalTun = true: the
// tun fd comes from VpnService and the OS owns the routes, so the generated tun
// inbound must omit auto_route. It is written explicitly below (not via a data-class
// default) so serialization can never drop it.
object ConfigGenerator {

    private val json = Json {
        ignoreUnknownKeys = true
        encodeDefaults = false
    }

    // These calls block: importSubscription does network I/O, generateConfig/
    // orderNodes are CPU-bound. Callers must run them off the main thread
    // (withContext(Dispatchers.IO)).

    fun version(): String = TenebraCore.version()

    // Fetch a subscription and return the FULL profile JSON the core builds from it
    // (credentials included — the app is the profile store). Stored verbatim and fed
    // back into generateConfig/orderNodes without re-encoding. See import.go.
    fun importSubscription(
        url: String,
        name: String? = null,
        headers: Map<String, String>? = null,
    ): String {
        val request = buildJsonObject {
            put("url", url)
            if (!name.isNullOrEmpty()) put("name", name)
            if (!headers.isNullOrEmpty()) {
                putJsonObject("headers") {
                    headers.forEach { (k, v) -> put(k, v) }
                }
            }
        }
        // JsonObject.toString() emits valid compact JSON.
        return TenebraCore.importSubscription(request.toString())
    }

    // Turn a stored profile (its raw JSON) plus a selected tag into a sing-box config
    // JSON, ready to hand to libbox unchanged. The profile is embedded as parsed JSON
    // so every field survives the round-trip exactly. See generate.go.
    fun generateConfig(
        rawProfileJson: String,
        selectedTag: String? = null,
        routing: RoutingConfig = RoutingConfig(),
        tun: TunConfig = TunConfig(),
    ): String {
        val profileElement = json.parseToJsonElement(rawProfileJson)
        val request = buildJsonObject {
            put("profile", profileElement)
            if (!selectedTag.isNullOrEmpty()) put("selectedTag", selectedTag)
            putJsonObject("routing") {
                put("mode", routing.mode)
                put("bypassLan", routing.bypassLan)
                put("ipv4Only", routing.ipv4Only)
                put("killSwitch", routing.killSwitch)
                if (routing.dnsRemote.isNotEmpty()) put("dnsRemote", routing.dnsRemote)
                if (routing.dnsDirect.isNotEmpty()) put("dnsDirect", routing.dnsDirect)
                if (routing.ruleSetDir.isNotEmpty()) put("ruleSetDir", routing.ruleSetDir)
            }
            putJsonObject("tun") {
                // Non-negotiable on Android. Explicit so it is never omitted.
                put("externalTun", true)
                if (tun.mtu > 0) put("mtu", tun.mtu)
                if (tun.stack.isNotEmpty()) put("stack", tun.stack)
                if (tun.interfaceName.isNotEmpty()) put("interfaceName", tun.interfaceName)
            }
        }
        return TenebraCore.generateConfig(request.toString())
    }

    // Ask the core for the profile's selector tags in anti-DPI fallback order, so the
    // native connect loop can retry nodes in the core's order via libbox
    // SelectOutbound rather than re-implementing the walk. See order.go.
    fun orderNodes(
        rawProfileJson: String,
        lastGood: String? = null,
        latency: Map<String, Long> = emptyMap(),
    ): List<String> {
        val profileElement = json.parseToJsonElement(rawProfileJson)
        val request = buildJsonObject {
            put("profile", profileElement)
            if (!lastGood.isNullOrEmpty()) put("lastGood", lastGood)
            if (latency.isNotEmpty()) {
                putJsonObject("latency") {
                    latency.forEach { (id, ms) -> put(id, ms) }
                }
            }
        }
        val result = TenebraCore.orderNodes(request.toString())
        return json.decodeFromString(result)
    }
}

// Gomobile-facing projection of routing.Options (core-bridge). Defaults to "global"
// so a first connect tunnels everything and needs no cached rule-sets; "smart" split
// routing requires ruleSetDir to point at the app's cached binary .srs rule-sets
// (mobile must read them locally, never fetch), which is a later wiring step.
data class RoutingConfig(
    val mode: String = "global",
    val bypassLan: Boolean = true,
    val ipv4Only: Boolean = false,
    val killSwitch: Boolean = false,
    val dnsRemote: String = "",
    val dnsDirect: String = "",
    val ruleSetDir: String = "",
)

// Gomobile-facing projection of singbox.TunOptions. mtu/stack left at 0/"" let the
// core normalize to its defaults; whatever it picks flows back to openTun via
// libbox's TunOptions, so the engine and the VpnService.Builder stay consistent by
// construction. externalTun is forced true at serialization time in generateConfig.
data class TunConfig(
    val interfaceName: String = "",
    val mtu: Int = 0,
    val stack: String = "",
    val externalTun: Boolean = true,
)
