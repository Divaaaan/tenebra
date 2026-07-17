//go:build ios || android

// This file is the thin gomobile surface for package tenebracore: the exported,
// string/error functions gomobile binds into the mobile artifacts. Each one just
// delegates to the pure implementation in the build-tag-free files (generate.go,
// import.go, order.go), where the logic and its host-runnable tests live. Keeping
// the exported wrappers here, behind the ios||android tag, is what isolates the
// gomobile specifics from the desktop build while leaving the logic testable on
// every host. See the package doc in generate.go for the wider design.
//
// gomobile renders these as free functions prefixed with the capitalized package
// name: `GenerateConfig` is `TenebracoreGenerateConfig(requestJSON, &error)` from
// Swift and `Tenebracore.generateConfig(requestJSON)` from Kotlin. Confirm the
// exact spelling against the generated headers/bindings.

package tenebracore

// GenerateConfig turns a profile (plus optional routing/tun options) into a
// sing-box configuration JSON string, ready to hand to libbox unchanged. See
// generateRequest for the envelope. On mobile, set tun.externalTun so the tun
// inbound omits auto_route — the platform owns routing.
func GenerateConfig(requestJSON string) (string, error) {
	return generateConfig(requestJSON)
}

// ImportSubscription fetches a subscription URL and returns the JSON of the profile
// built from it — its nodes plus any quota/expiry. See importRequest for the
// envelope. It is the "import a subscription, get a profile" call the app makes
// before GenerateConfig.
func ImportSubscription(requestJSON string) (string, error) {
	return importSubscription(requestJSON, defaultFetch)
}

// OrderNodes returns the profile's selector tags in anti-DPI fallback order as a
// JSON array, so the native connect loop tries nodes in the same order the core
// would rather than re-inventing it. See orderRequest for the envelope.
func OrderNodes(requestJSON string) (string, error) {
	return orderNodes(requestJSON)
}

// Version returns the binding's ABI version — the shape of these calls'
// request/response envelopes, distinct from the sing-box engine and app versions —
// so a mismatched host and binding can be detected.
func Version() string {
	return bridgeVersion
}
