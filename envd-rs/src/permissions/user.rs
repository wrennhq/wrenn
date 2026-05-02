use nix::unistd::{Gid, Group, Uid, User};

pub fn lookup_user(username: &str) -> Result<User, String> {
    User::from_name(username)
        .map_err(|e| format!("error looking up user '{username}': {e}"))?
        .ok_or_else(|| format!("user '{username}' not found"))
}

pub fn get_uid_gid(user: &User) -> (Uid, Gid) {
    (user.uid, user.gid)
}

pub fn get_user_groups(user: &User) -> Vec<Gid> {
    let c_name = std::ffi::CString::new(user.name.as_str()).unwrap();
    nix::unistd::getgrouplist(&c_name, user.gid).unwrap_or_default()
}

pub fn lookup_username_by_uid(uid: Uid) -> String {
    User::from_uid(uid)
        .ok()
        .flatten()
        .map(|u| u.name)
        .unwrap_or_else(|| uid.to_string())
}

pub fn lookup_groupname_by_gid(gid: Gid) -> String {
    Group::from_gid(gid)
        .ok()
        .flatten()
        .map(|g| g.name)
        .unwrap_or_else(|| gid.to_string())
}
