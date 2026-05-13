use dashmap::DashMap;
use std::sync::{Arc, RwLock};

pub struct Defaults {
    pub env_vars: Arc<DashMap<String, String>>,
    user: RwLock<String>,
    workdir: RwLock<Option<String>>,
}

impl Defaults {
    pub fn new(user: &str) -> Self {
        Self {
            env_vars: Arc::new(DashMap::new()),
            user: RwLock::new(user.to_string()),
            workdir: RwLock::new(None),
        }
    }

    pub fn user(&self) -> String {
        self.user.read().unwrap().clone()
    }

    pub fn set_user(&self, user: String) {
        *self.user.write().unwrap() = user;
    }

    pub fn workdir(&self) -> Option<String> {
        self.workdir.read().unwrap().clone()
    }

    pub fn set_workdir(&self, workdir: Option<String>) {
        *self.workdir.write().unwrap() = workdir;
    }
}

pub fn resolve_default_workdir(workdir: &str, default_workdir: Option<&str>) -> String {
    if !workdir.is_empty() {
        return workdir.to_string();
    }
    if let Some(dw) = default_workdir {
        return dw.to_string();
    }
    String::new()
}

pub fn resolve_default_username<'a>(
    username: Option<&'a str>,
    default_username: &'a str,
) -> Result<&'a str, &'static str> {
    if let Some(u) = username {
        return Ok(u);
    }
    if !default_username.is_empty() {
        return Ok(default_username);
    }
    Err("username not provided")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn workdir_explicit_overrides_default() {
        assert_eq!(resolve_default_workdir("/explicit", Some("/default")), "/explicit");
    }

    #[test]
    fn workdir_empty_uses_default() {
        assert_eq!(resolve_default_workdir("", Some("/default")), "/default");
    }

    #[test]
    fn workdir_empty_no_default_returns_empty() {
        assert_eq!(resolve_default_workdir("", None), "");
    }

    #[test]
    fn workdir_explicit_ignores_none_default() {
        assert_eq!(resolve_default_workdir("/explicit", None), "/explicit");
    }

    #[test]
    fn username_explicit_returns_explicit() {
        assert_eq!(resolve_default_username(Some("root"), "wrenn").unwrap(), "root");
    }

    #[test]
    fn username_none_uses_default() {
        assert_eq!(resolve_default_username(None, "wrenn").unwrap(), "wrenn");
    }

    #[test]
    fn username_none_empty_default_errors() {
        assert!(resolve_default_username(None, "").is_err());
    }

    #[test]
    fn username_some_overrides_empty_default() {
        assert_eq!(resolve_default_username(Some("root"), "").unwrap(), "root");
    }

    #[test]
    fn defaults_user_set_and_get() {
        let d = Defaults::new("initial");
        assert_eq!(d.user(), "initial");
        d.set_user("changed".into());
        assert_eq!(d.user(), "changed");
    }

    #[test]
    fn defaults_workdir_initially_none() {
        let d = Defaults::new("user");
        assert!(d.workdir().is_none());
        d.set_workdir(Some("/home".into()));
        assert_eq!(d.workdir().unwrap(), "/home");
    }
}
