fn main() {
    // Expose the build target triple so the sidecar client can find the core
    // binary Tauri lays down as `tenebra-core-<triple>`. TARGET is set by Cargo
    // for build scripts.
    let target = std::env::var("TARGET").unwrap_or_default();
    println!("cargo:rustc-env=TENEBRA_TARGET_TRIPLE={target}");

    tauri_build::build();
}
