use std::path::Path;
use std::process::{Command, Stdio};

fn git(args: &[&str]) -> Option<String> {
    let output = Command::new("git")
        .args(args)
        .stderr(Stdio::null())
        .output()
        .ok()?;
    if !output.status.success() {
        return None;
    }
    Some(String::from_utf8(output.stdout).ok()?.trim().to_owned())
}

fn checkout_sha() -> Option<String> {
    // An unpacked source archive can sit inside an unrelated Git checkout.
    if !Path::new("../../.git").exists() && !Path::new(".git").exists() {
        return None;
    }
    for name in ["HEAD", "packed-refs"]
        .into_iter()
        .map(str::to_owned)
        .chain(git(&["symbolic-ref", "-q", "HEAD"]))
    {
        if let Some(path) = git(&["rev-parse", "--path-format=absolute", "--git-path", &name]) {
            println!("cargo:rerun-if-changed={path}");
        }
    }
    git(&["rev-parse", "--verify", "HEAD"])
}

fn main() {
    println!("cargo:rerun-if-changed=build.rs");
    println!("cargo:rerun-if-env-changed=LINA_TURN_HOST_BUILD_SHA");
    let sha = checkout_sha()
        .or_else(|| std::env::var("LINA_TURN_HOST_BUILD_SHA").ok())
        .filter(|sha| {
            sha.len() == 40
                && sha.bytes().all(|byte| byte.is_ascii_hexdigit())
                && sha.bytes().any(|byte| byte != b'0')
        })
        .map_or_else(|| "unknown".to_owned(), |sha| sha.to_ascii_lowercase());
    println!("cargo:rustc-env=LINA_TURN_HOST_BUILD_SHA={sha}");
}
