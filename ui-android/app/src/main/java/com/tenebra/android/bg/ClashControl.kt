package com.tenebra.android.bg

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive

// The clash-api endpoint of the CURRENTLY running tunnel: its 127.0.0.1 base URL and
// the per-run secret that gates it, both read out of the generated config. The service
// publishes this on connect and clears it on stop, so the UI can steer the live
// selector (hot node-switch) only while there is a tunnel to talk to. Loopback only —
// these calls never leave the device, and the secret is why no other local app can
// read the user's connections or flip their exit.
object ClashControl {
    data class Endpoint(val baseUrl: String, val secret: String)

    @Volatile
    var endpoint: Endpoint? = null
        private set

    private val json = Json { ignoreUnknownKeys = true }

    // Capture experimental.clash_api from a freshly generated sing-box config. A config
    // without it (which our builder never emits) just leaves the endpoint unset, so the
    // hot-switch degrades to "saved, applies on next connect".
    fun setFromConfig(configJson: String) {
        endpoint = runCatching {
            val clash = json.parseToJsonElement(configJson)
                .jsonObject["experimental"]?.jsonObject
                ?.get("clash_api")?.jsonObject ?: return@runCatching null
            val controller = clash["external_controller"]?.jsonPrimitive?.content
                ?: return@runCatching null
            val secret = clash["secret"]?.jsonPrimitive?.content.orEmpty()
            Endpoint("http://$controller", secret)
        }.getOrNull()
    }

    fun clear() {
        endpoint = null
    }
}
