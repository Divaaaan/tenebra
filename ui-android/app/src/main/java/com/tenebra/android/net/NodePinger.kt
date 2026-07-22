package com.tenebra.android.net

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.withContext
import java.net.InetSocketAddress
import java.net.Socket

// TCP connect-time latency to each node, used to rank nodes and drive the AUTO choice.
// This is a hint, not a routed measurement: the probe is a plain socket, so it is
// meaningful while disconnected (picking a node before connecting). Once the tunnel is
// up these sockets ride it like any other app traffic, so callers refresh on the
// disconnected screen rather than trusting a live-tunnel reading.
object NodePinger {
    private const val TIMEOUT_MS = 3000

    // A target the pinger can reach: the node's id paired with its host and port.
    data class Target(val id: String, val host: String, val port: Int)

    // Connect-time latency in ms, or -1 if the node did not answer within the timeout.
    suspend fun ping(host: String, port: Int): Int = withContext(Dispatchers.IO) {
        val start = System.nanoTime()
        val reached = runCatching {
            Socket().use { it.connect(InetSocketAddress(host, port), TIMEOUT_MS) }
        }.isSuccess
        if (reached) ((System.nanoTime() - start) / 1_000_000).toInt().coerceAtLeast(1) else -1
    }

    // Ping every target concurrently; returns id -> latency ms (-1 = no answer). One
    // slow node never holds up the others — each probe is its own coroutine, bounded by
    // the per-connect timeout.
    suspend fun pingAll(targets: List<Target>): Map<String, Int> = coroutineScope {
        targets.map { t -> async { t.id to ping(t.host, t.port) } }.awaitAll().toMap()
    }
}
