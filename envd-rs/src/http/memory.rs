// POST /memory/preload — guest-side helper that materialises every physical
// RAM page so a subsequent ch.snapshot writes a self-contained memory-ranges
// file. Required after a restore with memory_restore_mode=ondemand: pages
// that were never demand-faulted live only in the source memory-ranges file
// and would become holes (read back as zero) in the new snapshot.
//
// Trigger is one-byte-per-page reads through /dev/mem (fallback /proc/kcore
// PT_LOAD segments). The guest kernel walks its direct map → accesses the
// physical page → host kernel handles the EPT entry → CH's userfaultfd
// handler fills the page from the source memory-ranges file.
//
// Wire protocol:
//   POST /memory/preload         — starts the loader (idempotent) and returns
//                                  the current status JSON immediately
//   GET  /memory/preload         — returns the current status JSON
//   POST /memory/preload/cancel  — signals the loader to stop early
//
// Returning immediately avoids any HTTP-level header timeout in the caller
// while materialisation (hundreds of MiB at one byte per page) runs in a
// background blocking thread.

use std::fs;
use std::io::{Read, Seek, SeekFrom};
use std::os::unix::fs::FileExt;
use std::sync::Arc;
use std::sync::atomic::Ordering;
use std::time::Instant;

use axum::Json;
use axum::extract::State;
use axum::http::StatusCode;
use axum::response::IntoResponse;
use serde::Serialize;

use crate::state::AppState;

const PAGE_SIZE: u64 = 4096;

#[derive(Serialize, Clone)]
pub struct PreloadStatus {
    /// One of: "idle", "running", "done", "failed", "cancelled".
    pub state: &'static str,
    pub regions: u64,
    pub pages: u64,
    pub bytes: u64,
    pub elapsed_sec: f64,
    pub source: &'static str,
    pub error: Option<String>,
}

pub async fn post_memory_preload(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    // First caller wins the CAS and spawns the loader; subsequent callers
    // just report the existing status.
    let we_start = state
        .mem_preload_started
        .compare_exchange(false, true, Ordering::SeqCst, Ordering::SeqCst)
        .is_ok();

    if we_start {
        let state_clone = Arc::clone(&state);
        // Detached blocking thread — no axum task lifetime ties it to the
        // request, so the connection can close immediately without aborting
        // materialisation. Lifecycle bump on the next /init clears the flags
        // for a fresh run after a restore.
        std::thread::spawn(move || {
            let started = Instant::now();
            match preload_blocking(&state_clone) {
                Ok((source, regions, pages, bytes)) => {
                    let elapsed = started.elapsed().as_secs_f64();
                    state_clone
                        .mem_preload_regions
                        .store(regions, Ordering::SeqCst);
                    state_clone.mem_preload_pages.store(pages, Ordering::SeqCst);
                    state_clone.mem_preload_bytes.store(bytes, Ordering::SeqCst);
                    state_clone
                        .mem_preload_elapsed_us
                        .store((elapsed * 1_000_000.0) as u64, Ordering::SeqCst);
                    set_source(&state_clone, source);
                    *state_clone.mem_preload_error.lock().unwrap() = None;
                    state_clone.mem_preload_done.store(true, Ordering::SeqCst);
                    tracing::info!(
                        regions,
                        pages,
                        bytes,
                        elapsed_sec = elapsed,
                        source,
                        "memory preload complete"
                    );
                }
                Err(e) => {
                    let elapsed = started.elapsed().as_secs_f64();
                    state_clone
                        .mem_preload_elapsed_us
                        .store((elapsed * 1_000_000.0) as u64, Ordering::SeqCst);
                    *state_clone.mem_preload_error.lock().unwrap() = Some(e.clone());
                    state_clone.mem_preload_done.store(true, Ordering::SeqCst);
                    tracing::warn!(error = %e, "memory preload failed");
                }
            }
        });
    }

    let status = read_status(&state);
    (StatusCode::OK, Json(status))
}

pub async fn get_memory_preload(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    (StatusCode::OK, Json(read_status(&state)))
}

pub async fn post_memory_preload_cancel(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    state.mem_preload_cancel.store(true, Ordering::SeqCst);
    StatusCode::NO_CONTENT
}

fn read_status(state: &AppState) -> PreloadStatus {
    let started = state.mem_preload_started.load(Ordering::SeqCst);
    let done = state.mem_preload_done.load(Ordering::SeqCst);
    let cancelled = state.mem_preload_cancel.load(Ordering::SeqCst);
    let error = state.mem_preload_error.lock().unwrap().clone();

    let lane = if !started {
        "idle"
    } else if !done {
        "running"
    } else if let Some(_) = &error {
        "failed"
    } else if cancelled {
        "cancelled"
    } else {
        "done"
    };

    PreloadStatus {
        state: lane,
        regions: state.mem_preload_regions.load(Ordering::SeqCst),
        pages: state.mem_preload_pages.load(Ordering::SeqCst),
        bytes: state.mem_preload_bytes.load(Ordering::SeqCst),
        elapsed_sec: state.mem_preload_elapsed_us.load(Ordering::SeqCst) as f64 / 1_000_000.0,
        source: get_source(state),
        error,
    }
}

fn set_source(state: &AppState, src: &'static str) {
    let code: u8 = match src {
        "/dev/mem" => 1,
        "/proc/kcore" => 2,
        _ => 0,
    };
    state.mem_preload_source.store(code, Ordering::SeqCst);
}

fn get_source(state: &AppState) -> &'static str {
    match state.mem_preload_source.load(Ordering::SeqCst) {
        1 => "/dev/mem",
        2 => "/proc/kcore",
        _ => "",
    }
}

fn preload_blocking(state: &AppState) -> Result<(&'static str, u64, u64, u64), String> {
    let ranges = parse_system_ram_ranges().map_err(|e| format!("iomem: {e}"))?;
    if ranges.is_empty() {
        return Err("no System RAM ranges found in /proc/iomem".into());
    }

    let mut pages: u64 = 0;
    let mut bytes: u64 = 0;

    match preload_via_devmem(&ranges, state, &mut pages, &mut bytes) {
        Ok(()) => Ok(("/dev/mem", ranges.len() as u64, pages, bytes)),
        Err(devmem_err) => {
            tracing::warn!(
                error = %devmem_err,
                "/dev/mem preload failed, falling back to /proc/kcore"
            );
            pages = 0;
            bytes = 0;
            preload_via_kcore(state, &mut pages, &mut bytes)
                .map_err(|e| format!("/dev/mem: {devmem_err}; /proc/kcore: {e}"))?;
            Ok(("/proc/kcore", ranges.len() as u64, pages, bytes))
        }
    }
}

fn parse_system_ram_ranges() -> std::io::Result<Vec<(u64, u64)>> {
    let data = fs::read_to_string("/proc/iomem")?;
    let mut out = Vec::new();
    for line in data.lines() {
        if line.starts_with(|c: char| c.is_whitespace()) {
            continue;
        }
        let Some((range, label)) = line.split_once(" : ") else {
            continue;
        };
        if label.trim() != "System RAM" {
            continue;
        }
        let Some((start, end)) = range.split_once('-') else {
            continue;
        };
        let start = u64::from_str_radix(start.trim(), 16).ok();
        let end = u64::from_str_radix(end.trim(), 16).ok();
        if let (Some(s), Some(e)) = (start, end) {
            out.push((s, e.saturating_add(1)));
        }
    }
    Ok(out)
}

fn preload_via_devmem(
    ranges: &[(u64, u64)],
    state: &AppState,
    pages: &mut u64,
    bytes: &mut u64,
) -> std::io::Result<()> {
    let f = fs::File::open("/dev/mem")?;
    let mut buf = [0u8; 1];
    for (start, end) in ranges {
        let mut off = *start;
        while off < *end {
            if state.mem_preload_cancel.load(Ordering::SeqCst) {
                return Ok(());
            }
            f.read_at(&mut buf, off)?;
            *pages += 1;
            *bytes += PAGE_SIZE;
            // Publish progress so GET /memory/preload reports useful numbers
            // while the loader is still running.
            if *pages % 1024 == 0 {
                state.mem_preload_pages.store(*pages, Ordering::SeqCst);
                state.mem_preload_bytes.store(*bytes, Ordering::SeqCst);
            }
            off = off.saturating_add(PAGE_SIZE);
        }
    }
    Ok(())
}

// Read /proc/kcore's direct-map segment to materialise physical RAM. The
// direct map's PT_LOAD covers the kernel's *maximum* possible direct-map
// region (64TB on x86_64), not just the present physical RAM — iterating the
// whole segment would loop for billions of pages. Bound the walk to the sum
// of System RAM ranges from /proc/iomem; sequential reads through the
// segment touch consecutive physical pages 1:1, which is what we need.
fn preload_via_kcore(state: &AppState, pages: &mut u64, bytes: &mut u64) -> std::io::Result<()> {
    let ram_ranges = parse_system_ram_ranges()?;
    if ram_ranges.is_empty() {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            "no System RAM ranges to bound kcore walk",
        ));
    }
    let total_ram_bytes: u64 = ram_ranges.iter().map(|(s, e)| e - s).sum();

    let mut f = fs::File::open("/proc/kcore")?;
    let segments = parse_kcore_pt_load(&mut f)?;
    if segments.is_empty() {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            "no PT_LOAD segments in /proc/kcore",
        ));
    }

    // Pick the direct map: highest-vaddr kernel-space segment large enough
    // to plausibly cover RAM. KASLR randomises the base, so don't hardcode it.
    // Kernel virtual addresses start at 0xffff800000000000 on x86_64; vmalloc
    // / modules sit above the direct map and are usually smaller.
    const KERNEL_SPACE_MIN: u64 = 0xffff_8000_0000_0000;
    let direct_map = segments
        .iter()
        .filter(|s| s.vaddr >= KERNEL_SPACE_MIN && s.file_size >= total_ram_bytes)
        .min_by_key(|s| s.vaddr)
        .ok_or_else(|| {
            std::io::Error::new(
                std::io::ErrorKind::InvalidData,
                format!(
                    "no PT_LOAD segment large enough for {} bytes of RAM in /proc/kcore",
                    total_ram_bytes
                ),
            )
        })?;

    let mut buf = [0u8; 1];
    let read_bytes = total_ram_bytes.min(direct_map.file_size);
    let start = direct_map.file_offset;
    let end = start.saturating_add(read_bytes);
    let mut off = start;
    while off < end {
        if state.mem_preload_cancel.load(Ordering::SeqCst) {
            return Ok(());
        }
        // Reads into MMIO holes within the direct map can fail; ignore so the
        // loop keeps making progress over the present RAM ranges either side.
        if f.read_at(&mut buf, off).is_ok() {
            *pages += 1;
            *bytes += PAGE_SIZE;
            if *pages % 256 == 0 {
                state.mem_preload_pages.store(*pages, Ordering::SeqCst);
                state.mem_preload_bytes.store(*bytes, Ordering::SeqCst);
            }
        }
        off = off.saturating_add(PAGE_SIZE);
    }
    Ok(())
}

struct KcoreSegment {
    file_offset: u64,
    file_size: u64,
    vaddr: u64,
}

fn parse_kcore_pt_load(f: &mut fs::File) -> std::io::Result<Vec<KcoreSegment>> {
    let mut hdr = [0u8; 64];
    f.seek(SeekFrom::Start(0))?;
    f.read_exact(&mut hdr)?;

    if &hdr[0..4] != b"\x7fELF" || hdr[4] != 2 {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            "not an ELF64 file",
        ));
    }

    let e_phoff = u64::from_le_bytes(hdr[32..40].try_into().unwrap());
    let e_phentsize = u16::from_le_bytes(hdr[54..56].try_into().unwrap()) as u64;
    let e_phnum = u16::from_le_bytes(hdr[56..58].try_into().unwrap()) as u64;

    let mut out = Vec::new();
    let mut entry = vec![0u8; e_phentsize as usize];
    for i in 0..e_phnum {
        f.seek(SeekFrom::Start(e_phoff + i * e_phentsize))?;
        f.read_exact(&mut entry)?;
        let p_type = u32::from_le_bytes(entry[0..4].try_into().unwrap());
        if p_type != 1 {
            continue;
        }
        let p_offset = u64::from_le_bytes(entry[8..16].try_into().unwrap());
        let p_vaddr = u64::from_le_bytes(entry[16..24].try_into().unwrap());
        let p_filesz = u64::from_le_bytes(entry[32..40].try_into().unwrap());
        out.push(KcoreSegment {
            file_offset: p_offset,
            file_size: p_filesz,
            vaddr: p_vaddr,
        });
    }
    Ok(out)
}
