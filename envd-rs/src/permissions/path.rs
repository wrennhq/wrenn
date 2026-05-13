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

#[cfg(test)]
mod tests {
    use super::*;

    // expand_tilde

    #[test]
    fn tilde_empty_passthrough() {
        assert_eq!(expand_tilde("", "/home/u").unwrap(), "");
    }

    #[test]
    fn tilde_no_tilde_passthrough() {
        assert_eq!(expand_tilde("/absolute", "/home/u").unwrap(), "/absolute");
    }

    #[test]
    fn tilde_bare() {
        assert_eq!(expand_tilde("~", "/home/user").unwrap(), "/home/user");
    }

    #[test]
    fn tilde_slash_path() {
        assert_eq!(expand_tilde("~/docs", "/home/user").unwrap(), "/home/user/docs");
    }

    #[test]
    fn tilde_nested() {
        assert_eq!(expand_tilde("~/a/b/c", "/h").unwrap(), "/h/a/b/c");
    }

    #[test]
    fn tilde_other_user_errors() {
        assert!(expand_tilde("~bob/foo", "/home/user").is_err());
    }

    #[test]
    fn tilde_relative_no_tilde() {
        assert_eq!(expand_tilde("relative/path", "/home/u").unwrap(), "relative/path");
    }

    // expand_and_resolve

    #[test]
    fn resolve_absolute_passthrough() {
        assert_eq!(expand_and_resolve("/abs/path", "/home", None).unwrap(), "/abs/path");
    }

    #[test]
    fn resolve_empty_uses_default() {
        assert_eq!(expand_and_resolve("", "/home", Some("/default")).unwrap(), "/default");
    }

    #[test]
    fn resolve_empty_no_default_falls_back_to_home() {
        // Empty path with no default → joins "" with home_dir → returns home_dir
        let result = expand_and_resolve("", "/home", None).unwrap();
        assert_eq!(result, "/home");
    }

    #[test]
    fn resolve_tilde_expands() {
        assert_eq!(expand_and_resolve("~/dir", "/home/u", None).unwrap(), "/home/u/dir");
    }

    #[test]
    fn resolve_relative_joins_home() {
        let result = expand_and_resolve("subdir", "/tmp", None).unwrap();
        // Relative path joined with home and canonicalized (or raw join on missing)
        assert!(result.starts_with("/tmp"));
        assert!(result.contains("subdir"));
    }

    #[test]
    fn resolve_tilde_other_user_errors() {
        assert!(expand_and_resolve("~bob", "/home/u", None).is_err());
    }

    // ensure_dirs

    #[test]
    fn ensure_dirs_creates_nested() {
        let tmp = tempfile::TempDir::new().unwrap();
        let path = tmp.path().join("a/b/c");
        let uid = nix::unistd::getuid();
        let gid = nix::unistd::getgid();
        ensure_dirs(path.to_str().unwrap(), uid, gid).unwrap();
        assert!(path.is_dir());
    }

    #[test]
    fn ensure_dirs_existing_is_ok() {
        let tmp = tempfile::TempDir::new().unwrap();
        let uid = nix::unistd::getuid();
        let gid = nix::unistd::getgid();
        ensure_dirs(tmp.path().to_str().unwrap(), uid, gid).unwrap();
    }

    #[test]
    fn ensure_dirs_file_in_path_errors() {
        let tmp = tempfile::TempDir::new().unwrap();
        let file_path = tmp.path().join("afile");
        std::fs::write(&file_path, "").unwrap();
        let nested = file_path.join("subdir");
        let uid = nix::unistd::getuid();
        let gid = nix::unistd::getgid();
        let result = ensure_dirs(nested.to_str().unwrap(), uid, gid);
        assert!(result.is_err());
        assert!(result.unwrap_err().contains("path is a file"));
    }
}
