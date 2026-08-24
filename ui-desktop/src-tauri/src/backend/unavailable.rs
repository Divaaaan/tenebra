//! The backend for "there is no core".
//!
//! It answers every command with the same refusal and holds no state at all.
//!
//! It exists because the alternative was worse: when the sidecar could not be
//! located or would not start, the app fell back to [`super::mock`] — the demo
//! fake — and the window then filled with invented profiles, a connect that
//! "succeeded" a second and a half later, and a bypass reporting fake strategies
//! and a fake "enabled". Every one of those is a claim about a machine nothing
//! was ever done to. A user in that state is being told the product works while
//! nothing is running, which is the one failure mode a VPN client must not have.
//!
//! Refusing instead costs nothing and says the true thing: the front end already
//! renders a failed opening snapshot as "the core cannot be reached, retrying"
//! (see `state/useTenebra.ts`), so a refusal here surfaces as exactly that,
//! carrying the reason the transport gave.
//!
//! The demo fake is still reachable — it is what `TENEBRA_MOCK=1` selects — but
//! only when someone asks for it by name.

use super::{
    Backend, ConnectionMode, ImportLinksResult, LeakCheck, NodeCheck, PingResult, Profile,
    RoutingMode, ServiceChecks, SpeedTest, SplitMode, State, StunCheck, SupportBundle, TunStack,
    ZapretActive, ZapretBundle, ZapretPick, ZapretUpdate,
};

/// A backend that refuses everything, naming why the core could not be reached.
pub struct UnavailableBackend {
    reason: String,
}

impl UnavailableBackend {
    /// `reason` is the transport's own failure text; it is repeated verbatim in
    /// every refusal so the UI can show what actually went wrong rather than a
    /// generic "unavailable".
    pub fn new(reason: impl Into<String>) -> Self {
        Self {
            reason: reason.into(),
        }
    }

    fn reason(&self) -> String {
        format!("tenebra-core is not running: {}", self.reason)
    }
}

impl Backend for UnavailableBackend {
    fn status(&self) -> Result<State, String> {
        Err(self.reason())
    }

    fn list_profiles(&self) -> Result<Vec<Profile>, String> {
        Err(self.reason())
    }

    fn import_subscription(&self, _url: String, _name: String) -> Result<Profile, String> {
        Err(self.reason())
    }

    fn import_link(&self, _link: String, _name: Option<String>) -> Result<Profile, String> {
        Err(self.reason())
    }

    fn import_links(
        &self,
        _links: Vec<String>,
        _name: Option<String>,
    ) -> Result<ImportLinksResult, String> {
        Err(self.reason())
    }

    fn remove_profile(&self, _profile: String) -> Result<(), String> {
        Err(self.reason())
    }

    fn refresh_subscription(&self, _profile: String) -> Result<Profile, String> {
        Err(self.reason())
    }

    fn connect(
        &self,
        _profile: String,
        _node: Option<String>,
        _auto: bool,
        _allow_tun_conflict: bool,
    ) -> Result<State, String> {
        Err(self.reason())
    }

    fn disconnect(&self) -> Result<State, String> {
        Err(self.reason())
    }

    fn ping(&self, _profile: String) -> Result<Vec<PingResult>, String> {
        Err(self.reason())
    }

    fn check_nodes(&self, _profile: String) -> Result<NodeCheck, String> {
        Err(self.reason())
    }

    fn check_services(&self) -> Result<ServiceChecks, String> {
        Err(self.reason())
    }

    fn set_routing(&self, _mode: RoutingMode) -> Result<State, String> {
        Err(self.reason())
    }

    fn set_split(&self, _mode: SplitMode, _apps: Vec<String>) -> Result<State, String> {
        Err(self.reason())
    }

    fn set_kill_switch(&self, _on: bool) -> Result<State, String> {
        Err(self.reason())
    }

    fn set_tls_fragment(&self, _on: bool) -> Result<State, String> {
        Err(self.reason())
    }

    fn set_multihop(
        &self,
        _profile: String,
        _enabled: bool,
        _entry_id: String,
        _exit_id: String,
    ) -> Result<State, String> {
        Err(self.reason())
    }

    fn set_tun(&self, _stack: TunStack) -> Result<State, String> {
        Err(self.reason())
    }

    fn set_proxy_mode(&self, _mode: ConnectionMode) -> Result<State, String> {
        Err(self.reason())
    }

    fn set_autoconnect(&self, _on: bool) -> Result<State, String> {
        Err(self.reason())
    }

    fn set_auto_failover(&self, _on: bool) -> Result<State, String> {
        Err(self.reason())
    }

    fn set_crash_reports(&self, _on: bool) -> Result<State, String> {
        Err(self.reason())
    }

    fn set_dns(
        &self,
        _ad_block: bool,
        _dns_remote: String,
        _dns_direct: String,
        _ipv4_only: bool,
    ) -> Result<State, String> {
        Err(self.reason())
    }

    fn set_rules(
        &self,
        _rules_direct: Vec<String>,
        _rules_proxy: Vec<String>,
        _preset_ru_banking: bool,
        _preset_ru_gov: bool,
    ) -> Result<State, String> {
        Err(self.reason())
    }

    fn set_presets(
        &self,
        _games: Option<bool>,
        _voice: Option<bool>,
        _services: Option<bool>,
    ) -> Result<State, String> {
        Err(self.reason())
    }

    fn leak_check(&self) -> Result<LeakCheck, String> {
        Err(self.reason())
    }

    fn run_stun_check(&self) -> Result<StunCheck, String> {
        Err(self.reason())
    }

    fn run_speed_test(&self) -> Result<SpeedTest, String> {
        Err(self.reason())
    }

    fn collect_diagnostics(&self) -> Result<SupportBundle, String> {
        Err(self.reason())
    }

    fn list_zapret(&self) -> Result<ZapretBundle, String> {
        Err(self.reason())
    }

    fn pick_zapret(&self) -> Result<ZapretPick, String> {
        Err(self.reason())
    }

    fn start_zapret(&self, _name: Option<String>) -> Result<ZapretActive, String> {
        Err(self.reason())
    }

    fn stop_zapret(&self) -> Result<(), String> {
        Err(self.reason())
    }

    fn update_zapret(&self) -> Result<ZapretUpdate, String> {
        Err(self.reason())
    }

    fn set_zapret_auto_update(&self, _on: bool) -> Result<State, String> {
        Err(self.reason())
    }
}

#[cfg(test)]
mod tests {
    //! What a machine with no reachable core must see. Each of these was
    //! previously answered by the demo mock, i.e. with an invention.

    use super::*;

    #[test]
    fn every_command_refuses() {
        let b = UnavailableBackend::new("could not locate tenebra-core: not found");

        // The opening snapshot is what the UI turns into "the core cannot be
        // reached, retrying". A backend that answers it with a state instead is
        // the whole of the lie: everything after that reads as a working app.
        assert!(b.status().is_err());
        // And nothing else invents anything either — no demo profiles, no
        // connect that succeeds on a timer, no bypass reporting strategies.
        assert!(b.list_profiles().is_err());
        assert!(b.connect("p1".into(), None, false, false).is_err());
        assert!(b.list_zapret().is_err());
        assert!(b.pick_zapret().is_err());
        assert!(b.start_zapret(None).is_err());
        assert!(b.stop_zapret().is_err());
        assert!(b.update_zapret().is_err());
    }

    #[test]
    fn the_refusal_names_what_went_wrong() {
        // A generic "unavailable" leaves a support conversation with nothing to
        // go on, so the transport's own reason travels into every refusal.
        let b = UnavailableBackend::new("could not start tenebra-core: access denied");
        let err = b.status().unwrap_err();
        assert!(err.contains("access denied"), "reason lost: {err}");
        assert!(err.contains("tenebra-core"), "no subject named: {err}");
    }
}
