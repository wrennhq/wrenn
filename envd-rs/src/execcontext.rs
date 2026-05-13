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
