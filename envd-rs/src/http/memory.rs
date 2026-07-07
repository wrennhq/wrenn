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
        // Fresh run: clear leftovers from a previous lifecycle's loader so a
        // stale done=true (or stale counters) can't masquerade as this run's
        // result. Done under the error mutex, the same critical section /init
        // and result publication use.
        {
            let mut err = state.mem_preload_error.lock().unwrap();
            *err = None;
            state.mem_preload_done.store(false, Ordering::SeqCst);
            state.mem_preload_regions.store(0, Ordering::SeqCst);
            state.mem_preload_pages.store(0, Ordering::SeqCst);
            state.mem_preload_bytes.store(0, Ordering::SeqCst);
            state.mem_preload_elapsed_us.store(0, Ordering::SeqCst);
            state.mem_preload_source.store(0, Ordering::SeqCst);
        }
        let generation = state.mem_preload_generation.load(Ordering::SeqCst);

        let state_clone = Arc::clone(&state);
        // Detached blocking thread — no axum task lifetime ties it to the
        // request, so the connection can close immediately without aborting
        // materialisation. Lifecycle bump on the next /init clears the flags
        // for a fresh run after a restore; this thread stops (and refuses to
        // publish) once the generation it captured is no longer current — a
        // pause can freeze it mid-walk and thaw it in the NEXT lifecycle.
        std::thread::spawn(move || {
            let started = Instant::now();
            let outcome = preload_blocking(&state_clone, generation);

            // Publish under the error mutex so a concurrent /init bump can't
            // interleave: either this store lands before the bump (and /init
            // resets it), or the generation no longer matches and the result
            // is discarded.
            let mut err = state_clone.mem_preload_error.lock().unwrap();
            if state_clone.mem_preload_generation.load(Ordering::SeqCst) != generation {
                tracing::info!(
                    generation,
                    "memory preload result discarded: stale lifecycle"
                );
                return;
            }
            match outcome {
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
                    *err = None;
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
                    *err = Some(e.clone());
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

fn preload_blocking(
    state: &AppState,
    generation: u64,
) -> Result<(&'static str, u64, u64, u64), String> {
    let ranges = parse_system_ram_ranges().map_err(|e| format!("iomem: {e}"))?;
    if ranges.is_empty() {
        return Err("no System RAM ranges found in /proc/iomem".into());
    }

    let mut pages: u64 = 0;
    let mut bytes: u64 = 0;

    match preload_via_devmem(&ranges, state, generation, &mut pages, &mut bytes) {
        Ok(()) => Ok(("/dev/mem", ranges.len() as u64, pages, bytes)),
        Err(devmem_err) => {
            tracing::warn!(
                error = %devmem_err,
                "/dev/mem preload failed, falling back to /proc/kcore"
            );
            pages = 0;
            bytes = 0;
            preload_via_kcore(state, generation, &mut pages, &mut bytes)
                .map_err(|e| format!("/dev/mem: {devmem_err}; /proc/kcore: {e}"))?;
            Ok(("/proc/kcore", ranges.len() as u64, pages, bytes))
        }
    }
}

// should_stop reports whether the walk must abort: an explicit cancel, or the
// lifecycle moved on (a pause froze this thread and a resume /init bumped the
// generation — continuing would fault pages nobody waits for and eventually
// publish a stale result).
fn should_stop(state: &AppState, generation: u64) -> bool {
    state.mem_preload_cancel.load(Ordering::SeqCst)
        || state.mem_preload_generation.load(Ordering::SeqCst) != generation
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
    generation: u64,
    pages: &mut u64,
    bytes: &mut u64,
) -> std::io::Result<()> {
    let f = fs::File::open("/dev/mem")?;
    let mut buf = [0u8; 1];
    for (start, end) in ranges {
        let mut off = *start;
        while off < *end {
            if should_stop(state, generation) {
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

// Read physical RAM through /proc/kcore's per-range direct-map segments.
//
// The kernel emits one KCORE_RAM PT_LOAD *per contiguous System RAM range*
// (walk_system_ram_range), each with vaddr = direct_map_base + phys_start and
// file_size = range size — there is NO single segment covering all of RAM.
// Segments must therefore be matched range-by-range against /proc/iomem;
// picking "a big kernel-space segment" instead lands on the vmalloc or
// vmemmap region, which the kernel zero-fills for unmapped addresses without
// ever touching a physical page — the reads "succeed" instantly and nothing
// is materialised. Matching failure is a hard error for the same reason:
// reading the wrong segment is indistinguishable from success.
fn preload_via_kcore(
    state: &AppState,
    generation: u64,
    pages: &mut u64,
    bytes: &mut u64,
) -> std::io::Result<()> {
    let ram_ranges = parse_system_ram_ranges()?;
    if ram_ranges.is_empty() {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            "no System RAM ranges to bound kcore walk",
        ));
    }

    let mut f = fs::File::open("/proc/kcore")?;
    let segments = parse_kcore_pt_load(&mut f)?;
    if segments.is_empty() {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            "no PT_LOAD segments in /proc/kcore",
        ));
    }

    let ram_segments = match_ram_segments(&ram_ranges, &segments)
        .map_err(|e| std::io::Error::new(std::io::ErrorKind::InvalidData, e))?;

    let mut buf = [0u8; 1];
    let mut read_errors: u64 = 0;
    for seg in &ram_segments {
        let end = seg.file_offset.saturating_add(seg.len);
        let mut off = seg.file_offset;
        while off < end {
            if should_stop(state, generation) {
                return Ok(());
            }
            match f.read_at(&mut buf, off) {
                Ok(_) => {
                    *pages += 1;
                    *bytes += PAGE_SIZE;
                    if *pages % 256 == 0 {
                        state.mem_preload_pages.store(*pages, Ordering::SeqCst);
                        state.mem_preload_bytes.store(*bytes, Ordering::SeqCst);
                    }
                }
                Err(_) => read_errors += 1,
            }
            off = off.saturating_add(PAGE_SIZE);
        }
    }

    // A few failed reads are tolerable (hwpoison, offline pages); a walk where
    // nothing was read means the segments were wrong — report it rather than
    // letting the host trust an empty "done".
    if *pages == 0 {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            format!("kcore walk read no pages ({read_errors} read errors)"),
        ));
    }
    if read_errors > 0 {
        tracing::warn!(
            read_errors,
            pages = *pages,
            "kcore preload skipped unreadable pages"
        );
    }
    Ok(())
}

// A resolved (file_offset, length) window in /proc/kcore corresponding to one
// System RAM range.
struct RamSegment {
    file_offset: u64,
    len: u64,
}

// Match every System RAM range from /proc/iomem to its KCORE_RAM PT_LOAD
// segment. RAM segments share a single direct-map base (vaddr = base +
// phys_start; base is KASLR-randomised, so it must be derived, not assumed).
// Candidate bases come from exact-size (segment, range) pairs; a base is
// accepted only if EVERY RAM range has a segment at base + start with the
// exact range size. This can never select vmalloc/vmemmap segments: their
// sizes (tens of TB) match no iomem range.
fn match_ram_segments(
    ranges: &[(u64, u64)],
    segments: &[KcoreSegment],
) -> Result<Vec<RamSegment>, String> {
    const KERNEL_SPACE_MIN: u64 = 0xffff_8000_0000_0000;

    let mut candidates: Vec<u64> = Vec::new();
    for s in segments.iter().filter(|s| s.vaddr >= KERNEL_SPACE_MIN) {
        for (start, end) in ranges {
            let len = end - start;
            if s.file_size == len && s.vaddr.checked_sub(*start).is_some() {
                candidates.push(s.vaddr - start);
            }
        }
    }
    candidates.sort_unstable();
    candidates.dedup();

    for base in &candidates {
        let mut out = Vec::with_capacity(ranges.len());
        for (start, end) in ranges {
            let len = end - start;
            let Some(seg) = segments
                .iter()
                .find(|s| s.vaddr == base.wrapping_add(*start) && s.file_size == len)
            else {
                out.clear();
                break;
            };
            out.push(RamSegment {
                file_offset: seg.file_offset,
                len,
            });
        }
        if out.len() == ranges.len() {
            return Ok(out);
        }
    }

    Err(format!(
        "no consistent direct-map base for {} RAM ranges across {} PT_LOAD segments \
         ({} candidate bases tried)",
        ranges.len(),
        segments.len(),
        candidates.len(),
    ))
}

struct KcoreSegment {
    file_offset: u64,
    file_size: u64,
    vaddr: u64,
}

#[cfg(test)]
mod tests {
    use super::*;

    const BASE: u64 = 0xffff_8880_0000_0000; // typical direct-map base, no KASLR
    const GIB: u64 = 1 << 30;
    const TIB: u64 = 1 << 40;

    fn seg(vaddr: u64, file_size: u64, file_offset: u64) -> KcoreSegment {
        KcoreSegment {
            file_offset,
            file_size,
            vaddr,
        }
    }

    // Typical CH guest: low RAM hole below 1MB, main range, plus the huge
    // vmalloc/vmemmap segments that the old heuristic used to pick.
    fn typical_segments() -> Vec<KcoreSegment> {
        vec![
            seg(0xffff_c900_0000_0000, 32 * TIB, 0x1000), // vmalloc
            seg(0xffff_ea00_0000_0000, TIB, 0x2000),      // vmemmap
            seg(BASE + 0x1000, 0x9e000, 0x10000),         // RAM 0x1000-0x9efff
            seg(BASE + 0x100000, 2 * GIB - 0x100000, 0x20000), // RAM 1MB-2GB
        ]
    }

    fn typical_ranges() -> Vec<(u64, u64)> {
        vec![(0x1000, 0x9f000), (0x100000, 2 * GIB)]
    }

    #[test]
    fn matches_all_ranges_and_skips_vmalloc() {
        let out = match_ram_segments(&typical_ranges(), &typical_segments()).unwrap();
        assert_eq!(out.len(), 2);
        assert_eq!(out[0].file_offset, 0x10000);
        assert_eq!(out[0].len, 0x9e000);
        assert_eq!(out[1].file_offset, 0x20000);
        assert_eq!(out[1].len, 2 * GIB - 0x100000);
    }

    #[test]
    fn kaslr_base_is_derived_not_assumed() {
        let kaslr = BASE + 37 * GIB; // PUD-granular randomisation
        let segments = vec![
            seg(0xffff_c900_0000_0000, 32 * TIB, 0x1000),
            seg(kaslr + 0x1000, 0x9e000, 0x10000),
            seg(kaslr + 0x100000, 2 * GIB - 0x100000, 0x20000),
        ];
        let out = match_ram_segments(&typical_ranges(), &segments).unwrap();
        assert_eq!(out[1].file_offset, 0x20000);
    }

    #[test]
    fn errors_when_a_range_has_no_segment() {
        // Second RAM range's segment missing → no base covers all ranges.
        let segments = vec![
            seg(0xffff_c900_0000_0000, 32 * TIB, 0x1000),
            seg(BASE + 0x1000, 0x9e000, 0x10000),
        ];
        assert!(match_ram_segments(&typical_ranges(), &segments).is_err());
    }

    #[test]
    fn errors_instead_of_falling_back_to_vmalloc() {
        // Only huge kernel segments present (the exact shape that fooled the
        // old `file_size >= total_ram` heuristic).
        let segments = vec![
            seg(0xffff_c900_0000_0000, 32 * TIB, 0x1000),
            seg(0xffff_ea00_0000_0000, TIB, 0x2000),
        ];
        assert!(match_ram_segments(&typical_ranges(), &segments).is_err());
    }

    #[test]
    fn ambiguous_same_size_ranges_still_resolve() {
        // Two RAM ranges of identical size: candidate bases from cross pairs
        // must be rejected; only the true base matches both ranges.
        let ranges = vec![(0x0, GIB), (2 * GIB, 3 * GIB)];
        let segments = vec![seg(BASE, GIB, 0x10000), seg(BASE + 2 * GIB, GIB, 0x20000)];
        let out = match_ram_segments(&ranges, &segments).unwrap();
        assert_eq!(out[0].file_offset, 0x10000);
        assert_eq!(out[1].file_offset, 0x20000);
    }
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
