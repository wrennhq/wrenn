use dashmap::DashMap;
use std::sync::Arc;

#[derive(Clone)]
pub struct Defaults {
    pub env_vars: Arc<DashMap<String, String>>,
    pub user: String,
    pub workdir: Option<String>,
}

impl Defaults {
    pub fn new(user: &str) -> Self {
        Self {
            env_vars: Arc::new(DashMap::new()),
            user: user.to_string(),
            workdir: None,
        }
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
