package com.tenebra.android.net

import com.tenebra.android.bg.ClashControl
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import java.net.HttpURLConnection
import java.net.URL

// Steers the running tunnel's selector over loopback so a tapped node takes effect
// live, with no reconnect. Authorised by the per-run secret the service captured from
// the config. Every call is best-effort: if it fails the selection is still saved and
// applies on the next connect, so a missed switch never leaves the user stuck.
object ClashApiClient {
    // The selector's tag in every config the generator emits (singbox proxyTag).
    private const val SELECTOR = "proxy"

    // Point the running selector at outboundTag. Returns true on a 2xx, false if there
    // is no live tunnel or the call did not take.
    suspend fun selectOutbound(outboundTag: String): Boolean = withContext(Dispatchers.IO) {
        val ep = ClashControl.endpoint ?: return@withContext false
        val body = buildJsonObject { put("name", outboundTag) }.toString()
        runCatching {
            val conn = URL("${ep.baseUrl}/proxies/$SELECTOR").openConnection() as HttpURLConnection
            try {
                conn.requestMethod = "PUT"
                conn.connectTimeout = 2000
                conn.readTimeout = 2000
                if (ep.secret.isNotEmpty()) {
                    conn.setRequestProperty("Authorization", "Bearer ${ep.secret}")
                }
                conn.setRequestProperty("Content-Type", "application/json")
                conn.doOutput = true
                conn.outputStream.use { it.write(body.toByteArray()) }
                conn.responseCode in 200..299
            } finally {
                conn.disconnect()
            }
        }.getOrDefault(false)
    }
}
