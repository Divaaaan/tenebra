package com.tenebra.android.bg

// Masks subscription tokens, node credentials and server addresses in the diagnostic
// text before it leaves the device (Copy/Share). The aim is a report that still shows
// the protocol, the error and the stack trace — enough to debug a failed connect —
// without the user's subscription or keys ending up in a chat.
//
// Deliberately light: an over-zealous scrub that also strips the error text helps no
// one, so this targets the shapes that are actually secret and leaves everything else
// (including DNS servers like 1.1.1.1 and stack frames) intact.
object DiagnosticsScrubber {

    private const val REDACTED = "<redacted>"

    // VMess/VLESS user IDs and other UUID-shaped credentials, anywhere in the text.
    private val UUID = Regex(
        "\\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\\b",
    )

    // Any http(s) URL. The credential in a managed subscription is the last path
    // segment (and/or the query), so we keep the scheme, host and leading path and drop
    // the tail — the host stays visible because it is useful and is not itself a secret.
    private val URL = Regex("https?://[^\\s\"'<>]+")

    // The credential-bearing keys of a stored profile (see core/model/model.go). Their
    // values are pulled out of the raw profile JSON and masked verbatim wherever they
    // appear in the log, which also catches a server address that happens to read like
    // an ordinary IP (masked here rather than by a blunt catch-all).
    private val SECRET_KEYS = Regex(
        "\"(?:password|uuid|private_key|pre_shared_key|server|url)\"\\s*:\\s*\"([^\"]+)\"",
        RegexOption.IGNORE_CASE,
    )

    // Exact secrets to mask, read from the raw profile JSON the app stores verbatim.
    fun secretsFromProfileJson(rawProfileJson: String?): List<String> {
        if (rawProfileJson.isNullOrBlank()) return emptyList()
        val out = LinkedHashSet<String>()
        runCatching {
            SECRET_KEYS.findAll(rawProfileJson).forEach { out += it.groupValues[1] }
        }
        // Skip very short values: they are more likely to collide with ordinary words
        // in a stack trace than to be a real secret.
        return out.filter { it.length >= 4 }
    }

    fun scrub(text: String, secrets: List<String>): String {
        var out = text
        // 1. Exact known secrets first, longest first so a value that contains a shorter
        //    one is masked whole.
        secrets.sortedByDescending { it.length }.forEach { out = out.replace(it, REDACTED) }
        // 2. Any leftover UUID-shaped credential the profile scan did not know about.
        out = UUID.replace(out) { REDACTED }
        // 3. Managed subscription token: keep host + directory, drop the last segment.
        out = URL.replace(out) { redactUrlTail(it.value) }
        return out
    }

    // Keep everything up to the last path separator; replace the final segment (and any
    // query/fragment) with the marker. A bare host with no path is left untouched.
    private fun redactUrlTail(url: String): String {
        val queryAt = url.indexOfFirst { it == '?' || it == '#' }
        val hasQuery = queryAt >= 0
        val base = if (hasQuery) url.substring(0, queryAt) else url
        val schemeEnd = base.indexOf("://").let { if (it < 0) 0 else it + 3 }
        val lastSlash = base.lastIndexOf('/')
        val hasSegment = lastSlash >= schemeEnd && lastSlash < base.length - 1
        return when {
            // scheme://host/dir/<token>[?query] -> keep the directory, drop the rest
            hasSegment -> base.substring(0, lastSlash + 1) + REDACTED
            // scheme://host[/]?<token> -> the query carried the token; drop it
            hasQuery -> (if (base.endsWith("/")) base else "$base/") + REDACTED
            // scheme://host with nothing secret after it -> leave it, it is diagnostic
            else -> base
        }
    }
}
