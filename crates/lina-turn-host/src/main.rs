mod host;
mod model;
mod protocol;
mod read_file;
mod turn;

fn main() -> std::process::ExitCode {
    // A blocking stdin reader must not keep explicit shutdown waiting for pipe EOF.
    let Ok(runtime) = tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
    else {
        eprintln!("turn host runtime setup failed");
        return std::process::ExitCode::FAILURE;
    };
    if runtime.block_on(host::run()).is_err() {
        eprintln!("turn host transport failed");
        runtime.shutdown_timeout(std::time::Duration::from_secs(1));
        return std::process::ExitCode::FAILURE;
    }
    runtime.shutdown_timeout(std::time::Duration::from_secs(1));
    std::process::ExitCode::SUCCESS
}
