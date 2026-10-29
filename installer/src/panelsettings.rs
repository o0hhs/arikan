//! The panel's own address as its database keeps it: settings.domain (or settings.public_host
//! without one) and settings.panel_port are what every link, the admin path and each node's
//! address are built from, while the port the panel actually listens on is PANEL_PORT in
//! config.json. A restore from another server brings the former and keeps the latter, so the
//! two are compared — and a port this host has free is taken, so that the panel answers where
//! the links lead.
//!
//! The settings are read through `mikan database address` in the panel's image: it reads the
//! database directly, so nothing here parses SQL, and it works while the panel is stopped (a
//! restore) as well as from `arikan doctor` on a running one.

use std::time::Duration;

use anyhow::{Context, Result, bail};
use serde::Deserialize;

/// Where the panel says it answers, as `mikan database address` prints it.
#[derive(Deserialize, Debug, Clone, PartialEq, Default)]
pub struct Address {
    #[serde(default)]
    pub domain: String,
    #[serde(default)]
    pub public_host: String,
    /// 0 when the database never had one: the panel builds no link at all then.
    #[serde(default)]
    pub panel_port: u16,
}

impl Address {
    /// The host the panel's links are built from: the domain, or the public address when it
    /// has none (settings.Endpoint in the panel).
    pub fn host(&self) -> &str {
        if self.domain.is_empty() { &self.public_host } else { &self.domain }
    }

    /// https://host:port, as the panel writes its address; "" while a part of it is missing.
    pub fn url(&self) -> String {
        let host = self.host();
        if host.is_empty() || self.panel_port == 0 {
            return String::new();
        }
        let host = if host.contains(':') && !host.starts_with('[') { format!("[{host}]") } else { host.to_owned() };
        format!("https://{host}:{}", self.panel_port)
    }
}

/// `mikan database address`, given at most limit to start its container and answer.
pub fn read(limit: Duration) -> Result<Address> {
    let out = crate::docker::database_within(&["address"], limit)?;
    parse(&String::from_utf8_lossy(&out.stdout))
}

/// The address in the command's output: `docker compose run` may put lines of its own
/// around the one the command prints.
fn parse(stdout: &str) -> Result<Address> {
    let Some(line) = stdout.lines().rev().map(str::trim).find(|l| l.starts_with('{')) else {
        bail!("`mikan database address` printed no address");
    };
    serde_json::from_str(line).context("`mikan database address` printed an address this arikan does not read")
}

/// What a server should do about its listening port, given the one the database names.
#[derive(Debug, Clone, Copy, PartialEq)]
pub enum PortChoice {
    /// The database names no port: nothing to go by, the server keeps its own.
    Unset,
    /// The database names the port the server listens on.
    Same(u16),
    /// Another port, free here: take it, so that every link and every node's address lead
    /// where the panel answers.
    Take(u16),
    /// Another port, and something here holds it: the panel cannot answer there. It keeps
    /// its own listener, and the panel corrects the setting at its next start
    /// (internal/panel/app/serve.go).
    Held(u16),
}

/// The decision without a server around it: own is PANEL_PORT from config.json, db is
/// settings.panel_port, and free says whether this host holds a port. A missing port and 0
/// are the same thing: no port.
pub fn port_choice(own: Option<u16>, db: Option<u16>, free: impl FnOnce(u16) -> bool) -> PortChoice {
    let Some(db) = db.filter(|p| *p > 0) else { return PortChoice::Unset };
    let own = own.filter(|p| *p > 0);
    if own == Some(db) {
        return PortChoice::Same(db);
    }
    if free(db) { PortChoice::Take(db) } else { PortChoice::Held(db) }
}

#[cfg(test)]
mod tests {
    use super::*;

    // The address the panel reports is read from one line of JSON, whatever else the
    // container printed around it.
    #[test]
    fn the_address_the_panel_reports() {
        let a = parse("{\"domain\":\"vpn.example.com\",\"public_host\":\"203.0.113.5\",\"panel_port\":2053}\n").unwrap();
        assert_eq!((a.host(), a.url()), ("vpn.example.com", "https://vpn.example.com:2053".to_owned()));
        let noisy =
            "time=\"…\" level=warning msg=\"a compose warning\"\n{\"domain\":\"\",\"public_host\":\"203.0.113.5\",\"panel_port\":2053}\n";
        let a = parse(noisy).unwrap();
        assert_eq!(a.url(), "https://203.0.113.5:2053");
        // A database that never had the port, or an image that prints fewer fields.
        let a = parse("{\"domain\":\"vpn.example.com\"}").unwrap();
        assert_eq!((a.panel_port, a.url().as_str()), (0, ""));
        let a = parse("{}").unwrap();
        assert_eq!((a.host(), a.url().as_str()), ("", ""));
        // IPv6 is bracketed as the panel brackets it in a link.
        let a = Address { public_host: "2001:db8::1".into(), panel_port: 2053, ..Default::default() };
        assert_eq!(a.url(), "https://[2001:db8::1]:2053");
        assert!(parse("").is_err());
        assert!(parse("not an address").is_err());
    }

    // The port to listen on after a restore: the database's when this host has it free,
    // the server's own when it does not, and nothing to do when they agree.
    #[test]
    fn the_port_a_restored_database_names() {
        let free = |_: u16| true;
        let held = |_: u16| false;
        assert_eq!(port_choice(Some(21355), Some(2053), free), PortChoice::Take(2053));
        assert_eq!(port_choice(Some(21355), Some(2053), held), PortChoice::Held(2053));
        assert_eq!(port_choice(Some(2053), Some(2053), free), PortChoice::Same(2053));
        // The same port is not taken again, held or not: the panel is where it was.
        assert_eq!(port_choice(Some(2053), Some(2053), held), PortChoice::Same(2053));
        // A database without the setting, or with 0, says nothing.
        assert_eq!(port_choice(Some(21355), None, free), PortChoice::Unset);
        assert_eq!(port_choice(Some(21355), Some(0), free), PortChoice::Unset);
        assert_eq!(port_choice(None, None, free), PortChoice::Unset);
        // A server whose config.json lost PANEL_PORT takes the database's port when it is
        // free, and has nothing to listen on when it is not.
        assert_eq!(port_choice(None, Some(2053), free), PortChoice::Take(2053));
        assert_eq!(port_choice(Some(0), Some(2053), free), PortChoice::Take(2053));
        assert_eq!(port_choice(None, Some(2053), held), PortChoice::Held(2053));
    }
}
