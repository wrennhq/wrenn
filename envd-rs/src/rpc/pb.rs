#![allow(dead_code, non_camel_case_types, unused_imports, clippy::derivable_impls)]

use ::buffa;
use ::buffa_types;
use ::connectrpc;
use ::futures;
use ::http_body;
use ::serde;

include!(concat!(env!("OUT_DIR"), "/_connectrpc.rs"));
