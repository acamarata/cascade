//! `cascade ping [--echo <text>] [--machine]` — hidden health check command.
//!
//! Sends a Ping request to the daemon and displays the response.
//! Used by monitoring scripts, the doctor check, and daemon startup verification.
//!
//! Output format (plain text, default):
//! ```text
//! pong: <echo or empty>
//! ```
//!
//! With `--machine` flag: raw JSON output.
//!
//! Exit code `0` on success; `1` if daemon is not running.

use async_trait::async_trait;
use cascade_types::error::Result;
use cascade_types::ipc::{PingParams, PingResult};
use clap::Args;

use super::Command;
use crate::ipc_client::IpcClient;

/// Arguments for `cascade ping`.
#[derive(Debug, Args)]
pub struct PingArgs {
    /// Optional payload to echo back.
    #[arg(long)]
    pub echo: Option<String>,

    /// Output as raw JSON instead of human-readable format.
    #[arg(long)]
    pub machine: bool,
}

#[async_trait]
impl Command for PingArgs {
    async fn run(&self) -> Result<()> {
        let client = IpcClient::new()
            .map_err(|e| cascade_types::error::CascadeError::Other(e.to_string()))?;

        let params = PingParams {
            echo: self.echo.clone(),
        };

        let result = client.send::<PingParams, PingResult>("ping", params).await;

        match result {
            Ok(pong) => {
                if self.machine {
                    // Output raw JSON.
                    let json_str = serde_json::to_string_pretty(&pong)
                        .map_err(|e| cascade_types::error::CascadeError::Other(e.to_string()))?;
                    println!("{}", json_str);
                } else {
                    // Human-readable format.
                    println!("pong: {}", pong.pong);
                }
                Ok(())
            }
            Err(crate::ipc_client::IpcClientError::DaemonNotRunning) => {
                eprintln!("cascade daemon is not running. Start it with: cascade daemon start");
                std::process::exit(1);
            }
            Err(e) => {
                eprintln!("Error: {}", e);
                std::process::exit(1);
            }
        }
    }
}

#[cfg(test)]
mod tests {
    #[test]
    fn ping_args_parses_without_echo() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(subcommand)]
            cmd: crate::cmd::Commands,
        }

        let args = vec!["cascade", "ping"];
        let cli = Cli::try_parse_from(args).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Ping(ping_args) => {
                assert_eq!(ping_args.echo, None);
                assert!(!ping_args.machine);
            }
            _ => panic!("expected Ping command"),
        }
    }

    #[test]
    fn ping_args_parses_with_echo() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(subcommand)]
            cmd: crate::cmd::Commands,
        }

        let args = vec!["cascade", "ping", "--echo", "hello"];
        let cli = Cli::try_parse_from(args).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Ping(ping_args) => {
                assert_eq!(ping_args.echo, Some("hello".to_string()));
                assert!(!ping_args.machine);
            }
            _ => panic!("expected Ping command"),
        }
    }

    #[test]
    fn ping_args_parses_with_machine_flag() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(subcommand)]
            cmd: crate::cmd::Commands,
        }

        let args = vec!["cascade", "ping", "--machine"];
        let cli = Cli::try_parse_from(args).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Ping(ping_args) => {
                assert_eq!(ping_args.echo, None);
                assert!(ping_args.machine);
            }
            _ => panic!("expected Ping command"),
        }
    }

    #[test]
    fn ping_args_parses_with_both_options() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(subcommand)]
            cmd: crate::cmd::Commands,
        }

        let args = vec!["cascade", "ping", "--echo", "world", "--machine"];
        let cli = Cli::try_parse_from(args).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Ping(ping_args) => {
                assert_eq!(ping_args.echo, Some("world".to_string()));
                assert!(ping_args.machine);
            }
            _ => panic!("expected Ping command"),
        }
    }
}
