mod build_service_payload;

use std::path::PathBuf;

fn main() {
    // Expose the build target triple so the sidecar client can find the core
    // binary Tauri lays down as `tenebra-core-<triple>`. TARGET is set by Cargo
    // for build scripts.
    let target = std::env::var("TARGET").unwrap_or_default();
    println!("cargo:rustc-env=TENEBRA_TARGET_TRIPLE={target}");

    write_service_payload_binding();
    tauri_build::build();
}

fn write_service_payload_binding() {
    println!("cargo:rerun-if-env-changed=TAURI_CONFIG");
    let overrides = std::env::var("TAURI_CONFIG")
        .map(|config| serde_json::from_str(&config).expect("invalid TAURI_CONFIG"))
        .unwrap_or_default();
    let bound = build_service_payload::has_bound_resources(&overrides)
        .expect("invalid service payload resource mapping");
    let contents = if bound {
        assert!(
            matches!(std::env::var("CARGO_CFG_TARGET_OS").as_deref(), Ok("macos")),
            "opaque service resources require a macOS target"
        );
        let manifest_dir = PathBuf::from(
            std::env::var_os("CARGO_MANIFEST_DIR").expect("Cargo manifest directory"),
        );
        let directory = manifest_dir.join(build_service_payload::BINDING_DIRECTORY);
        let config = manifest_dir.join("tauri.conf.json");
        println!("cargo:rerun-if-changed={}", config.display());
        for name in [
            "binding.json",
            "tenebra-helper",
            "helper.gz",
            "payload/payload.json",
            "payload.tar.gz",
        ] {
            println!("cargo:rerun-if-changed={}", directory.join(name).display());
        }
        build_service_payload::verified_binding(
            &directory,
            &config,
            &overrides,
            "darwin-universal",
            env!("CARGO_PKG_VERSION"),
        )
        .expect("prepare the sealed macOS service payload before bundling")
    } else {
        "None\n".to_owned()
    };
    let out = PathBuf::from(std::env::var_os("OUT_DIR").expect("Cargo output directory"));
    std::fs::write(out.join("service_payload_binding.rs"), contents)
        .expect("write compiled service payload binding");
}
