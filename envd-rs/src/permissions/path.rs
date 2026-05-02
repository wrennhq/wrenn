use std::fs;
use std::os::unix::fs::chown;
use std::path::{Path, PathBuf};

use nix::unistd::{Gid, Uid};

fn expand_tilde(path: &str, home_dir: &str) -> Result<String, String> {
    if path.is_empty() || !path.starts_with('~') {
        return Ok(path.to_string());
    }
    if path.len() > 1 && path.as_bytes()[1] != b'/' && path.as_bytes()[1] != b'\\' {
        return Err("cannot expand user-specific home dir".into());
    }
    Ok(format!("{}{}", home_dir, &path[1..]))
}

pub fn expand_and_resolve(
    path: &str,
    home_dir: &str,
    default_path: Option<&str>,
) -> Result<String, String> {
    let path = if path.is_empty() {
        default_path.unwrap_or("").to_string()
    } else {
        path.to_string()
    };

    let path = expand_tilde(&path, home_dir)?;

    if Path::new(&path).is_absolute() {
        return Ok(path);
    }

    let joined = PathBuf::from(home_dir).join(&path);
    joined
        .canonicalize()
        .or_else(|_| Ok(joined))
        .map(|p| p.to_string_lossy().to_string())
}

pub fn ensure_dirs(path: &str, uid: Uid, gid: Gid) -> Result<(), String> {
    let path = Path::new(path);
    let mut current = PathBuf::new();

    for component in path.components() {
        current.push(component);
        let current_str = current.to_string_lossy();

        if current_str == "/" {
            continue;
        }

        match fs::metadata(&current) {
            Ok(meta) => {
                if !meta.is_dir() {
                    return Err(format!("path is a file: {current_str}"));
                }
            }
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                fs::create_dir(&current)
                    .map_err(|e| format!("failed to create directory {current_str}: {e}"))?;
                chown(&current, Some(uid.as_raw()), Some(gid.as_raw()))
                    .map_err(|e| format!("failed to chown directory {current_str}: {e}"))?;
            }
            Err(e) => {
                return Err(format!("failed to stat directory {current_str}: {e}"));
            }
        }
    }

    Ok(())
}
