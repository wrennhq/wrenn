fn main() {
    connectrpc_build::Config::new()
        .files(&[
            "../proto/envd/process.proto",
            "../proto/envd/filesystem.proto",
        ])
        .includes(&["../proto/envd", "/usr/include"])
        .include_file("_connectrpc.rs")
        .compile()
        .unwrap();
}
