use std::sync::Arc;
use std::time::Duration;

use axum::Json;
use axum::extract::State;
use axum::http::{StatusCode, header};
use axum::response::{IntoResponse, Response};
use serde::Deserialize;

use crate::state::AppState;

#[derive(Deserialize)]
pub struct MountRequest {
    /// virtio-blk serial set by the host in the VM config; used to resolve the
    /// block device via /sys/block/*/serial regardless of enumeration order.
    pub serial: String,
    /// Guest path to mount the volume at (e.g. "/mnt/vol-...").
    pub mount_path: String,
}

#[derive(Deserialize)]
pub struct UnmountRequest {
    pub mount_path: String,
}

fn json_error(status: StatusCode, msg: &str) -> Response {
    let body = serde_json::json!({ "code": status.as_u16(), "message": msg });
    (status, Json(body)).into_response()
}

fn no_content() -> Response {
    (
        StatusCode::NO_CONTENT,
        [(header::CACHE_CONTROL, "no-store")],
    )
        .into_response()
}

/// POST /volumes/mount — called by the host agent after boot for each attached
/// volume. Resolves the block device by its virtio-blk serial, formats it with
/// ext4 only if it has no filesystem yet (existing data is never reformatted),
/// then mounts it at the requested path.
pub async fn post_mount(
    State(_state): State<Arc<AppState>>,
    Json(req): Json<MountRequest>,
) -> Response {
    // Serials are host-generated hex tokens; reject anything else before it
    // reaches a sysfs comparison / command argument.
    if req.serial.is_empty() || !req.serial.chars().all(|c| c.is_ascii_alphanumeric()) {
        return json_error(StatusCode::BAD_REQUEST, "invalid volume serial");
    }
    if req.mount_path.is_empty() {
        return json_error(StatusCode::BAD_REQUEST, "mount_path is required");
    }

    let device = match resolve_device_by_serial(&req.serial).await {
        Some(d) => d,
        None => {
            return json_error(
                StatusCode::NOT_FOUND,
                &format!("no block device with serial {}", req.serial),
            );
        }
    };

    // blkid exit status: 0 = a filesystem/signature is present (leave it alone),
    // 2 = no recognized signature (format it), anything else = probe error.
    match has_filesystem(&device).await {
        Ok(true) => {
            tracing::info!(device, "volume already has a filesystem; skipping mkfs");
        }
        Ok(false) => {
            if let Err(e) = mkfs_ext4(&device).await {
                return json_error(StatusCode::INTERNAL_SERVER_ERROR, &e);
            }
        }
        Err(e) => {
            return json_error(StatusCode::INTERNAL_SERVER_ERROR, &e);
        }
    }

    if let Err(e) = tokio::fs::create_dir_all(&req.mount_path).await {
        return json_error(
            StatusCode::INTERNAL_SERVER_ERROR,
            &format!("create mount dir: {e}"),
        );
    }

    // No -t: let the kernel autodetect the filesystem, so a pre-existing
    // non-ext4 volume still mounts.
    match tokio::process::Command::new("mount")
        .arg(&device)
        .arg(&req.mount_path)
        .output()
        .await
    {
        Ok(out) if out.status.success() => {
            tracing::info!(device, mount_path = %req.mount_path, "volume mounted");
            no_content()
        }
        Ok(out) => {
            let stderr = String::from_utf8_lossy(&out.stderr);
            // Already mounted at the target is success for an idempotent retry.
            if stderr.contains("already mounted") {
                return no_content();
            }
            json_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                &format!("mount failed: {}", stderr.trim()),
            )
        }
        Err(e) => json_error(
            StatusCode::INTERNAL_SERVER_ERROR,
            &format!("mount command failed: {e}"),
        ),
    }
}

/// POST /volumes/unmount — called best-effort before a graceful capsule destroy.
/// Flushes buffered writes (sync) and unmounts so the backing file is
/// consistent. Idempotent: unmounting an already-unmounted path is fine.
pub async fn post_unmount(
    State(_state): State<Arc<AppState>>,
    Json(req): Json<UnmountRequest>,
) -> Response {
    if req.mount_path.is_empty() {
        return json_error(StatusCode::BAD_REQUEST, "mount_path is required");
    }

    // sync flushes all filesystems, guaranteeing the volume's data reaches its
    // backing file even if the umount below is skipped or fails.
    let _ = tokio::task::spawn_blocking(nix::unistd::sync).await;

    let _ = tokio::process::Command::new("umount")
        .arg(&req.mount_path)
        .output()
        .await;

    no_content()
}

/// resolve_device_by_serial finds the /dev path of the virtio-blk device whose
/// serial matches. Polls briefly since a boot-time disk may not be fully
/// enumerated the instant envd is asked to mount it.
async fn resolve_device_by_serial(serial: &str) -> Option<String> {
    for _ in 0..50 {
        if let Some(dev) = find_block_by_serial(serial) {
            return Some(dev);
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    None
}

fn find_block_by_serial(serial: &str) -> Option<String> {
    let entries = std::fs::read_dir("/sys/block").ok()?;
    for entry in entries.flatten() {
        let name = entry.file_name();
        let name = name.to_string_lossy();
        // Only virtio-blk devices carry a serial we set; skip loop/ram/etc.
        if !name.starts_with("vd") {
            continue;
        }
        let serial_path = format!("/sys/block/{name}/serial");
        if let Ok(s) = std::fs::read_to_string(&serial_path) {
            if s.trim() == serial {
                return Some(format!("/dev/{name}"));
            }
        }
    }
    None
}

/// has_filesystem returns true when blkid detects any filesystem signature on
/// the device. Guards mkfs so existing data is never reformatted.
async fn has_filesystem(device: &str) -> Result<bool, String> {
    let out = tokio::process::Command::new("blkid")
        .arg(device)
        .output()
        .await
        .map_err(|e| format!("blkid spawn failed: {e}"))?;
    match out.status.code() {
        // 0 with output = a signature was printed. Guard on the output too so a
        // blkid variant that always exits 0 (e.g. busybox) is handled: empty
        // output means no filesystem.
        Some(0) => Ok(!String::from_utf8_lossy(&out.stdout).trim().is_empty()),
        // util-linux blkid: 2 = nothing detected.
        Some(2) => Ok(false),
        // 4 (usage) / 8 (error) / signal — treat as a probe failure.
        other => Err(format!(
            "blkid probe failed (exit {:?}): {}",
            other,
            String::from_utf8_lossy(&out.stderr).trim()
        )),
    }
}

async fn mkfs_ext4(device: &str) -> Result<(), String> {
    // -F: the target is a whole virtio-blk device, not a partition; without it
    // mkfs.ext4 prompts and would hang. Safe here — only reached when blkid
    // found no existing signature.
    let out = tokio::process::Command::new("mkfs.ext4")
        .args(["-q", "-F", device])
        .output()
        .await
        .map_err(|e| format!("mkfs.ext4 spawn failed: {e}"))?;
    if out.status.success() {
        tracing::info!(device, "formatted volume with ext4");
        Ok(())
    } else {
        Err(format!(
            "mkfs.ext4 failed: {}",
            String::from_utf8_lossy(&out.stderr).trim()
        ))
    }
}
