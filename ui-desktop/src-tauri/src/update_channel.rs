//! Channel-aware update check.
//!
//! Tauri's updater endpoint is fixed at build time (see tauri.conf.json →
//! plugins.updater → endpoints) and the plugin's JS `check()` exposes no runtime
//! override, so on its own it can only ever reach the stable manifest. This
//! module adds the beta channel: it overrides the updater endpoint for a single
//! check to the requested channel's manifest, then hands the front end the same
//! metadata the plugin's own check would — including the resource id of a real
//! `Update`, so the download, install and minisign verification that follow are
//! byte-for-byte the stable path.

use serde::Serialize;
use tauri::{Manager, ResourceId, Webview};
use tauri_plugin_updater::UpdaterExt;
use url::Url;

/// Base location the signed channel manifests are published to. Both manifests
/// live beside the installer on the latest GitHub release, so the stable one
/// keeps the exact URL that installed 0.3.0 clients already poll.
const MANIFEST_BASE: &str = "https://github.com/Divaaaan/tenebra/releases/latest/download";

/// The manifest URL for a release channel. `beta` resolves to `beta.json`;
/// every other value — `stable`, and anything unrecognised — resolves to
/// `latest.json`, so a stale or malformed channel can only ever fall back to
/// the safe stable manifest, never to an unintended endpoint.
fn manifest_url(channel: &str) -> String {
    let file = if channel == "beta" {
        "beta.json"
    } else {
        "latest.json"
    };
    format!("{MANIFEST_BASE}/{file}")
}

/// The `Update` fields the front end needs to rebuild a handle. Mirrors the
/// updater plugin's own check response (camelCase), so the JS side can pass it
/// straight to the `Update` constructor. `date` is intentionally omitted — the
/// front end never reads it, and the full server response is still available in
/// `raw_json` for anything that later does.
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ChannelUpdate {
    rid: ResourceId,
    current_version: String,
    version: String,
    body: Option<String>,
    raw_json: serde_json::Value,
}

/// Check the given channel's signed manifest for a newer release. Overrides the
/// updater endpoint to that manifest for this one check; on a hit the `Update`
/// is stored in the webview's resource table and its id returned, so the front
/// end's `downloadAndInstall` (routed back through the updater plugin) verifies
/// and installs it exactly as on the stable path. Resolves to `None` when the
/// running version is already current for the channel.
#[tauri::command]
pub async fn check_update_for_channel(
    webview: Webview,
    channel: String,
) -> Result<Option<ChannelUpdate>, String> {
    let endpoint = Url::parse(&manifest_url(&channel)).map_err(|e| e.to_string())?;
    let updater = webview
        .updater_builder()
        .endpoints(vec![endpoint])
        .map_err(|e| e.to_string())?
        .build()
        .map_err(|e| e.to_string())?;

    match updater.check().await.map_err(|e| e.to_string())? {
        Some(update) => {
            let meta = ChannelUpdate {
                current_version: update.current_version.clone(),
                version: update.version.clone(),
                body: update.body.clone(),
                raw_json: update.raw_json.clone(),
                // Storing the Update yields the id the plugin's download/install
                // commands look up; the front end rebuilds its Update from this.
                rid: webview.resources_table().add(update),
            };
            Ok(Some(meta))
        }
        None => Ok(None),
    }
}

#[cfg(test)]
mod tests {
    use super::manifest_url;

    const LATEST: &str = "https://github.com/Divaaaan/tenebra/releases/latest/download/latest.json";

    #[test]
    fn beta_resolves_to_the_beta_manifest() {
        assert_eq!(
            manifest_url("beta"),
            "https://github.com/Divaaaan/tenebra/releases/latest/download/beta.json"
        );
    }

    #[test]
    fn stable_resolves_to_the_latest_manifest() {
        assert_eq!(manifest_url("stable"), LATEST);
    }

    #[test]
    fn unknown_channels_fall_back_to_stable() {
        // A stale or malformed channel must never reach an unintended endpoint:
        // everything that isn't exactly "beta" resolves to the stable manifest.
        for channel in ["", "BETA", "Beta", "nightly", "latest", "stable"] {
            assert_eq!(
                manifest_url(channel),
                LATEST,
                "channel {channel:?} should resolve to the stable manifest",
            );
        }
    }
}
