use std::sync::Arc;
use std::time::Duration;

use dashmap::DashMap;
use serde::Deserialize;
use tokio_util::sync::CancellationToken;

use crate::config::{MMDS_ADDRESS, MMDS_POLL_INTERVAL, MMDS_TOKEN_EXPIRATION_SECS, WRENN_RUN_DIR};

#[derive(Debug, Clone, Deserialize)]
pub struct MMDSOpts {
    #[serde(rename = "instanceID")]
    pub sandbox_id: String,
    #[serde(rename = "envID")]
    pub template_id: String,
    #[serde(rename = "address")]
    pub logs_collector_address: String,
    #[serde(rename = "accessTokenHash", default)]
    pub access_token_hash: String,
}

async fn get_mmds_token(client: &reqwest::Client) -> Result<String, String> {
    let resp = client
        .put(format!("http://{MMDS_ADDRESS}/latest/api/token"))
        .header(
            "X-metadata-token-ttl-seconds",
            MMDS_TOKEN_EXPIRATION_SECS.to_string(),
        )
        .send()
        .await
        .map_err(|e| format!("mmds token request failed: {e}"))?;

    let token = resp.text().await.map_err(|e| format!("mmds token read: {e}"))?;
    if token.is_empty() {
        return Err("mmds token is an empty string".into());
    }
    Ok(token)
}

async fn get_mmds_opts(client: &reqwest::Client, token: &str) -> Result<MMDSOpts, String> {
    let resp = client
        .get(format!("http://{MMDS_ADDRESS}"))
        .header("X-metadata-token", token)
        .header("Accept", "application/json")
        .send()
        .await
        .map_err(|e| format!("mmds opts request failed: {e}"))?;

    resp.json::<MMDSOpts>()
        .await
        .map_err(|e| format!("mmds opts parse: {e}"))
}

pub async fn get_access_token_hash() -> Result<String, String> {
    let client = reqwest::Client::builder()
        .timeout(Duration::from_secs(10))
        .no_proxy()
        .build()
        .map_err(|e| format!("http client: {e}"))?;

    let token = get_mmds_token(&client).await?;
    let opts = get_mmds_opts(&client, &token).await?;
    Ok(opts.access_token_hash)
}

/// Polls MMDS every 50ms until metadata is available.
/// Stores sandbox_id and template_id in env_vars and writes to /run/wrenn/ files.
pub async fn poll_for_opts(
    env_vars: Arc<DashMap<String, String>>,
    cancel: CancellationToken,
) -> Option<MMDSOpts> {
    let client = reqwest::Client::builder()
        .no_proxy()
        .build()
        .ok()?;

    let mut interval = tokio::time::interval(MMDS_POLL_INTERVAL);

    loop {
        tokio::select! {
            _ = cancel.cancelled() => {
                tracing::warn!("context cancelled while waiting for mmds opts");
                return None;
            }
            _ = interval.tick() => {
                let token = match get_mmds_token(&client).await {
                    Ok(t) => t,
                    Err(e) => {
                        tracing::debug!(error = %e, "mmds token poll");
                        continue;
                    }
                };

                let opts = match get_mmds_opts(&client, &token).await {
                    Ok(o) => o,
                    Err(e) => {
                        tracing::debug!(error = %e, "mmds opts poll");
                        continue;
                    }
                };

                env_vars.insert("WRENN_SANDBOX_ID".into(), opts.sandbox_id.clone());
                env_vars.insert("WRENN_TEMPLATE_ID".into(), opts.template_id.clone());

                let run_dir = std::path::Path::new(WRENN_RUN_DIR);
                let _ = std::fs::write(run_dir.join(".WRENN_SANDBOX_ID"), &opts.sandbox_id);
                let _ = std::fs::write(run_dir.join(".WRENN_TEMPLATE_ID"), &opts.template_id);

                return Some(opts);
            }
        }
    }
}
