use std::os::unix::fs::MetadataExt;
use std::path::Path;

use connectrpc::{ConnectError, ErrorCode};

use crate::permissions::user::{lookup_groupname_by_gid, lookup_username_by_uid};
use crate::rpc::pb::filesystem::{EntryInfo, FileType};
use nix::unistd::{Gid, Uid};

const NFS_SUPER_MAGIC: i64 = 0x6969;
const CIFS_MAGIC: i64 = 0xFF534D42;
const SMB_SUPER_MAGIC: i64 = 0x517B;
const SMB2_MAGIC_NUMBER: i64 = 0xFE534D42;
const FUSE_SUPER_MAGIC: i64 = 0x65735546;

pub fn is_network_mount(path: &str) -> Result<bool, String> {
    let c_path = std::ffi::CString::new(path).map_err(|e| e.to_string())?;
    let mut stat: libc::statfs = unsafe { std::mem::zeroed() };
    let ret = unsafe { libc::statfs(c_path.as_ptr(), &mut stat) };
    if ret != 0 {
        return Err(format!(
            "statfs {path}: {}",
            std::io::Error::last_os_error()
        ));
    }
    let fs_type = stat.f_type as i64;
    Ok(matches!(
        fs_type,
        NFS_SUPER_MAGIC | CIFS_MAGIC | SMB_SUPER_MAGIC | SMB2_MAGIC_NUMBER | FUSE_SUPER_MAGIC
    ))
}

pub fn build_entry_info(path: &str) -> Result<EntryInfo, ConnectError> {
    let p = Path::new(path);

    let lstat = std::fs::symlink_metadata(p).map_err(|e| {
        if e.kind() == std::io::ErrorKind::NotFound {
            ConnectError::new(ErrorCode::NotFound, format!("file not found: {e}"))
        } else {
            ConnectError::new(ErrorCode::Internal, format!("error getting file info: {e}"))
        }
    })?;

    let is_symlink = lstat.file_type().is_symlink();

    let (file_type, mode, symlink_target) = if is_symlink {
        let target = std::fs::canonicalize(p)
            .map(|t| t.to_string_lossy().to_string())
            .unwrap_or_else(|_| path.to_string());

        let target_type = match std::fs::metadata(p) {
            Ok(meta) => meta_to_file_type(&meta),
            Err(_) => FileType::FILE_TYPE_UNSPECIFIED,
        };

        let target_mode = std::fs::metadata(p)
            .map(|m| m.mode() & 0o7777)
            .unwrap_or(0);

        (target_type, target_mode, Some(target))
    } else {
        let ft = meta_to_file_type(&lstat);
        let mode = lstat.mode() & 0o7777;
        (ft, mode, None)
    };

    let uid = lstat.uid();
    let gid = lstat.gid();
    let owner = lookup_username_by_uid(Uid::from_raw(uid));
    let group = lookup_groupname_by_gid(Gid::from_raw(gid));

    let modified_time = {
        let mtime_sec = lstat.mtime();
        let mtime_nsec = lstat.mtime_nsec() as i32;
        if mtime_sec == 0 && mtime_nsec == 0 {
            None
        } else {
            Some(buffa_types::google::protobuf::Timestamp {
                seconds: mtime_sec,
                nanos: mtime_nsec,
                ..Default::default()
            })
        }
    };

    let name = p
        .file_name()
        .map(|n| n.to_string_lossy().to_string())
        .unwrap_or_default();

    let permissions = format_permissions(lstat.mode());

    Ok(EntryInfo {
        name,
        r#type: buffa::EnumValue::Known(file_type),
        path: path.to_string(),
        size: lstat.len() as i64,
        mode,
        permissions,
        owner,
        group,
        modified_time: modified_time.into(),
        symlink_target: symlink_target,
        ..Default::default()
    })
}

fn meta_to_file_type(meta: &std::fs::Metadata) -> FileType {
    if meta.is_file() {
        FileType::FILE_TYPE_FILE
    } else if meta.is_dir() {
        FileType::FILE_TYPE_DIRECTORY
    } else if meta.file_type().is_symlink() {
        FileType::FILE_TYPE_SYMLINK
    } else {
        FileType::FILE_TYPE_UNSPECIFIED
    }
}

fn format_permissions(mode: u32) -> String {
    let file_type = match mode & libc::S_IFMT {
        libc::S_IFDIR => 'd',
        libc::S_IFLNK => 'L',
        libc::S_IFREG => '-',
        libc::S_IFBLK => 'b',
        libc::S_IFCHR => 'c',
        libc::S_IFIFO => 'p',
        libc::S_IFSOCK => 'S',
        _ => '?',
    };

    let perms = mode & 0o777;
    let mut s = String::with_capacity(10);
    s.push(file_type);
    for shift in [6, 3, 0] {
        let bits = (perms >> shift) & 7;
        s.push(if bits & 4 != 0 { 'r' } else { '-' });
        s.push(if bits & 2 != 0 { 'w' } else { '-' });
        s.push(if bits & 1 != 0 { 'x' } else { '-' });
    }
    s
}
