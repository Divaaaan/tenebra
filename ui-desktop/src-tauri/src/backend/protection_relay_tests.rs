use super::*;
use serde_json::json;

#[test]
fn protection_survives_state_deserialization_and_ui_serialization() {
    for status in [
        "off",
        "applying",
        "active",
        "blocked",
        "error",
        "unavailable",
    ] {
        let protection = json!({ "status": status, "enforced": status == "active", "persistent": true, "error": "diagnostic" });
        // This is the same State decode and sink serialization used by the
        // status response and wire::forward_event -> TauriSink::state.
        let incoming = json!({ "event": "state", "state": "connected", "protection": protection });
        let state: State = serde_json::from_value(incoming).unwrap();
        let outgoing = serde_json::to_value(state).unwrap();
        assert_eq!(outgoing.get("protection"), Some(&protection));
    }
}

#[test]
fn old_core_without_protection_remains_unknown() {
    let state: State = serde_json::from_value(json!({ "state": "connected" })).unwrap();
    let outgoing = serde_json::to_value(state).unwrap();
    assert!(outgoing.get("protection").is_none());
}
