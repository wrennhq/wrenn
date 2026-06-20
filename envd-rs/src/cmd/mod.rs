//! Client subcommands for the `envd` binary. These run as short-lived
//! invocations (e.g. `envd ports`) inside the guest, separate from the
//! long-running daemon, and exit when done.

pub mod ports;
