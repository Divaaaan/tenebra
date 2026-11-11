use serde::Deserialize;
use sha2::{Digest, Sha256};
use std::fs::{self, File};
use std::io::{self, Read};
use std::path::Path;

const MIB: u64 = 1 << 20;
const MAX_METADATA: u64 = 64 << 10;
pub(crate) const BINDING_DIRECTORY: &str = "resources/service-payload";
const BUNDLE_RESOURCES: [(&str, &str); 2] = [
    ("resources/service-payload/helper.gz", "resources/helper.gz"),
    (
        "resources/service-payload/payload.tar.gz",
        "resources/payload.tar.gz",
    ),
];

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Binding {
    helper_sha256: String,
    helper_blob_sha256: String,
    payload_sha256: String,
    payload_archive_sha256: String,
    project_pubkey: String,
    target: String,
    version: String,
}

fn invalid(message: impl Into<String>) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, message.into())
}

fn open_regular(path: &Path, max: u64) -> io::Result<(File, u64)> {
    let named = fs::symlink_metadata(path)?;
    if !named.is_file() || named.len() == 0 || named.len() > max {
        return Err(invalid(format!(
            "invalid build resource {}",
            path.display()
        )));
    }
    let file = File::open(path)?;
    if file.metadata()?.len() != named.len() {
        return Err(invalid(format!(
            "build resource changed {}",
            path.display()
        )));
    }
    Ok((file, named.len()))
}

fn read_metadata(path: &Path) -> io::Result<Vec<u8>> {
    let (file, size) = open_regular(path, MAX_METADATA)?;
    let mut data = Vec::with_capacity(size as usize);
    file.take(size + 1).read_to_end(&mut data)?;
    if data.len() as u64 != size {
        return Err(invalid(format!(
            "build metadata changed {}",
            path.display()
        )));
    }
    Ok(data)
}

fn verify_digest(path: &Path, max: u64, digest: &str) -> io::Result<()> {
    if digest.len() != 64
        || !digest
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
    {
        return Err(invalid("invalid service payload digest"));
    }
    let (file, size) = open_regular(path, max)?;
    let mut reader = file.take(size + 1);
    let mut sha = Sha256::new();
    let mut buffer = [0; 32 << 10];
    let mut seen = 0;
    loop {
        let count = reader.read(&mut buffer)?;
        if count == 0 {
            break;
        }
        sha.update(&buffer[..count]);
        seen += count as u64;
    }
    if seen != size || format!("{:x}", sha.finalize()) != digest {
        return Err(invalid(format!(
            "service payload digest mismatch for {}",
            path.display()
        )));
    }
    Ok(())
}

pub(crate) fn has_bound_resources(config: &serde_json::Value) -> io::Result<bool> {
    let Some(resources) = config["bundle"]["resources"].as_object() else {
        return Ok(false);
    };
    let configured = resources.iter().any(|(from, to)| {
        BUNDLE_RESOURCES.iter().any(|(expected_from, expected_to)| {
            from.as_str() == *expected_from || to.as_str() == Some(*expected_to)
        })
    });
    if !configured {
        return Ok(false);
    }
    for (from, to) in BUNDLE_RESOURCES {
        if resources.get(from).and_then(|value| value.as_str()) != Some(to)
            || resources
                .values()
                .filter(|value| value.as_str() == Some(to))
                .count()
                != 1
        {
            return Err(invalid("invalid opaque service resource mapping"));
        }
    }
    Ok(true)
}

pub(crate) fn verified_binding(
    directory: &Path,
    config: &Path,
    overrides: &serde_json::Value,
    target: &str,
    version: &str,
) -> io::Result<String> {
    let binding: Binding =
        serde_json::from_slice(&read_metadata(&directory.join("binding.json"))?)?;
    let config: serde_json::Value = serde_json::from_slice(&read_metadata(config)?)?;
    if binding.target != target
        || binding.version != version
        || config["version"].as_str() != Some(version)
        || overrides
            .get("version")
            .is_some_and(|value| value.as_str() != Some(version))
    {
        return Err(invalid(
            "service payload target or version does not match this build",
        ));
    }
    let key = Some(binding.project_pubkey.as_str());
    if binding.project_pubkey.is_empty()
        || config["plugins"]["updater"]["pubkey"].as_str() != key
        || overrides
            .pointer("/plugins/updater/pubkey")
            .is_some_and(|value| value.as_str() != key)
    {
        return Err(invalid("service payload and updater public keys differ"));
    }
    for (name, maximum, digest) in [
        ("tenebra-helper", 64 * MIB, &binding.helper_sha256),
        ("helper.gz", 32 * MIB, &binding.helper_blob_sha256),
        (
            "payload/payload.json",
            MAX_METADATA,
            &binding.payload_sha256,
        ),
        (
            "payload.tar.gz",
            1024 * MIB,
            &binding.payload_archive_sha256,
        ),
    ] {
        verify_digest(&directory.join(name), maximum, digest)?;
    }
    Ok(format!(
        "Some(BootstrapBinding {{ helper_sha256: {:?}, helper_blob_sha256: {:?}, \
         payload_sha256: {:?}, payload_archive_sha256: {:?}, project_pubkey: {:?}, \
         target: {:?}, version: {:?} }})\n",
        binding.helper_sha256,
        binding.helper_blob_sha256,
        binding.payload_sha256,
        binding.payload_archive_sha256,
        binding.project_pubkey,
        binding.target,
        binding.version,
    ))
}
