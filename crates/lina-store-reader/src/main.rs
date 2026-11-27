mod search;

use std::env;
use std::io::{self, BufReader, Write as _};
use std::process::ExitCode;
use std::time::Duration;

use postgres::{Config, NoTls};
use search::{Hit, Request, patterns, search};

#[derive(Debug, thiserror::Error)]
enum Error {
    #[error("invalid request JSON")]
    Input,
    #[error("LINA_STORE_DSN must specify a synthetic lina_app connection")]
    Dsn,
    #[error("could not connect to the store")]
    Connect,
    #[error("store read failed")]
    Read,
    #[error("could not write response JSON")]
    Output,
}

fn run() -> Result<(), Error> {
    let requests: Vec<Request> =
        serde_json::from_reader(BufReader::new(io::stdin().lock())).map_err(|_| Error::Input)?;
    // The serial batch owns one connection, borrowed exclusively for each request.
    let mut connection = None;
    let mut results: Vec<Vec<Hit>> = Vec::with_capacity(requests.len());
    for request in &requests {
        let patterns = patterns(&request.query);
        if patterns.is_empty()
            || request.sources.is_empty()
            || request.authors.as_ref().is_some_and(Vec::is_empty)
        {
            results.push(Vec::new());
            continue;
        }

        let client = match &mut connection {
            Some(client) => client,
            slot @ None => {
                let dsn = env::var("LINA_STORE_DSN").map_err(|_| Error::Dsn)?;
                let mut config: Config = dsn.trim().parse().map_err(|_| Error::Dsn)?;
                if config.get_user() != Some("lina_app") {
                    return Err(Error::Dsn);
                }
                let client = config
                    .connect_timeout(Duration::from_secs(10))
                    .connect(NoTls)
                    .map_err(|_| Error::Connect)?;
                slot.insert(client)
            }
        };
        let mut transaction = client.transaction().map_err(|_| Error::Read)?;
        transaction
            .batch_execute("SET TRANSACTION READ ONLY")
            .map_err(|_| Error::Read)?;
        transaction
            .query_one(
                "SELECT set_config('lina.agent_id', $1, true)",
                &[&request.agent_id],
            )
            .map_err(|_| Error::Read)?;
        let hits = search(&mut transaction, request, &patterns).map_err(|_| Error::Read)?;
        transaction.commit().map_err(|_| Error::Read)?;
        results.push(hits);
    }
    let mut output = io::stdout().lock();
    serde_json::to_writer(&mut output, &results).map_err(|_| Error::Output)?;
    output.write_all(b"\n").map_err(|_| Error::Output)?;
    output.flush().map_err(|_| Error::Output)
}

fn main() -> ExitCode {
    match run() {
        Ok(()) => ExitCode::SUCCESS,
        Err(error) => {
            eprintln!("lina-store-reader: {error}");
            ExitCode::FAILURE
        }
    }
}
