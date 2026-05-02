fn main() {
    connectrpc_build::Config::new()
        .files(&["/usr/include/google/protobuf/timestamp.proto"])
        .includes(&["/usr/include"])
        .include_file("_types.rs")
        .emit_register_fn(false)
        .compile()
        .unwrap();
}
