//! The panel's address as the node sees it, and the ufw rule that keeps a node's API port
//! for it alone. The node writes down the addresses its panel connects from
//! (data/node/panel-addr): the installer replaces the rule that opens the port to everyone
//! with one rule per address, and puts the wide rule back when the panel does not come back
//! afterwards — a rule change must never cut a panel off from its node.

use std::net::IpAddr;
use std::path::Path;
use std::time::{Duration, Instant};

use anyhow::Result;
use serde::Deserialize;

use crate::ops::Install;
use crate::{DIR, clock, host, panelfs};

/// How long the panel is given to connect again after the rules changed. The node answers
/// the panel's health check every few seconds and writes the file at most this often, so a
/// working panel shows up well inside it.
pub const WAIT: Duration = Duration::from_secs(30);

/// The node's file, in its data directory on the host.
const FILE: &str = "panel-addr";

/// What the node writes: the addresses, most recent first.
#[derive(Deserialize, Debug, Clone, PartialEq, Default)]
pub struct Seen {
    #[serde(default)]
    pub ips: Vec<Addr>,
}

#[derive(Deserialize, Debug, Clone, PartialEq)]
pub struct Addr {
    pub ip: String,
    /// When the panel last connected from it, RFC 3339.
    #[serde(default)]
    pub last: Option<String>,
}

impl Seen {
    /// The addresses the node wrote down that parse as addresses, newest first.
    pub fn addrs(&self) -> Vec<IpAddr> {
        self.ips.iter().filter_map(|a| a.ip.parse().ok()).collect()
    }

    /// When the panel was last seen, in unix seconds.
    pub fn last(&self) -> Option<i64> {
        clock::parse_rfc3339(self.ips.first()?.last.as_deref()?)
    }
}

/// Reads the file the node wrote; None when there is none (a node its panel has not reached
/// yet) or it holds nothing readable.
pub fn read(root: &Path) -> Option<Seen> {
    let dir = panelfs::Dir::open(root, "data/node", false).ok()??;
    let raw = dir.read(FILE, 16 << 10).ok()??;
    let seen: Seen = serde_json::from_slice(&raw).ok()?;
    (!seen.ips.is_empty()).then_some(seen)
}

/// What the node on this server says now.
pub fn current() -> Option<Seen> {
    read(Path::new(DIR))
}

/// Forgets what the node saw, before it joins another panel: the addresses of the panel it
/// was on are not the ones the new one reaches it from, and the old panel has no business
/// staying in the firewall. The node writes the file again on its next connection.
pub fn forget(root: &Path) -> bool {
    if let Ok(Some(dir)) = panelfs::Dir::open(root, "data/node", false) {
        return dir.discard(FILE).is_ok();
    }
    false
}

/// What `scope` did, for the caller to say.
#[derive(Debug, Clone, PartialEq)]
pub enum Scoped {
    /// The port is behind these addresses now.
    Applied(Vec<IpAddr>),
    /// Nothing was changed: why.
    Nothing(String),
    /// The panel did not come back in time and the rule for everyone is in place again.
    Reverted,
}

/// Puts the node's API port behind the addresses the panel reaches it from. `explicit` are
/// addresses the admin named (--panel-ip): they are allowed next to the observed ones, so a
/// wrong one breaks nothing. Reports what it did; a change is verified by waiting for the
/// panel's next connection and taken back when it does not come.
pub fn scope(install: &mut Install, explicit: &[IpAddr], wait: Duration, say: &mut dyn FnMut(&str)) -> Result<Scoped> {
    let Some(port) = install.node_port() else {
        return Ok(Scoped::Nothing("no node API port".into()));
    };
    if !install.ufw() {
        return Ok(Scoped::Nothing("MIKAN_UFW=0".into()));
    }
    if !crate::system::ufw_active() {
        return Ok(Scoped::Nothing("ufw is off".into()));
    }
    let before = current();
    let mut wanted = before.as_ref().map(Seen::addrs).unwrap_or_default();
    for ip in explicit {
        if !wanted.contains(ip) {
            wanted.push(*ip);
        }
    }
    if wanted.is_empty() {
        return Ok(Scoped::Nothing(format!(
            "the panel has not connected yet ({} appears when it does); the port stays open, run `arikan update` then",
            FILE
        )));
    }
    let Some(status) = crate::system::ufw_status() else {
        return Ok(Scoped::Nothing("ufw status is unreadable".into()));
    };
    let plan = host::port_plan(&status, port, &wanted);
    if plan.nothing() {
        return Ok(Scoped::Applied(wanted));
    }
    let since = before.as_ref().and_then(Seen::last).unwrap_or(0);
    host::apply_plan(&plan)?;
    if came_back(since, wait) {
        install.set_panel_ips(&wanted)?;
        return Ok(Scoped::Applied(wanted));
    }
    // Nobody came: it may be the rule, it may be the panel itself; either way the node must
    // not be the one that cannot be reached.
    let back = host::PortPlan { add: vec![format!("{port}/tcp")], delete: wanted.iter().map(|ip| host::from_rule(*ip, port)).collect() };
    host::apply_plan(&back)?;
    say(&format!("the panel did not connect within {}s after the rules changed: port {port} is open to everyone again", wait.as_secs()));
    Ok(Scoped::Reverted)
}

/// Puts the node's API port behind the panel's addresses and says what happened. A ufw that
/// does not take a rule is said, not returned: it must not fail the change that called this.
pub fn scope_saying(install: &mut Install, explicit: &[IpAddr], say: &mut dyn FnMut(&str)) {
    match scope(install, explicit, WAIT, say) {
        Ok(Scoped::Applied(ips)) => {
            let list = ips.iter().map(ToString::to_string).collect::<Vec<_>>().join(", ");
            say(&format!("the node's API port is open for the panel alone ({list})"));
        }
        Ok(Scoped::Nothing(why)) => say(&format!("the node's API port stays open: {why}")),
        Ok(Scoped::Reverted) => {} // scope said why the rule went back
        Err(e) => say(&format!("ufw did not take the rule keeping the API port for the panel ({e:#}): it stays as it was")),
    }
}

/// Removes every rule this installer writes for a port: for a node that joins another panel
/// (the API moves to a new port) and for an uninstall (nothing listens there any more).
pub fn forget_port(port: u16) {
    let Some(status) = crate::system::ufw_status() else { return };
    let plan = host::port_plan(&status, port, &[]);
    if !plan.nothing() {
        let _ = host::apply_plan(&plan);
    }
}

/// Whether the node wrote down a connection newer than `since`: the panel got through.
fn came_back(since: i64, wait: Duration) -> bool {
    came_back_at(current, since, wait)
}

fn came_back_at(read: impl Fn() -> Option<Seen>, since: i64, wait: Duration) -> bool {
    let deadline = Instant::now() + wait;
    loop {
        if read().and_then(|s| s.last()).is_some_and(|t| t > since) {
            return true;
        }
        if Instant::now() >= deadline {
            return false;
        }
        std::thread::sleep(Duration::from_secs(2));
    }
}

/// The rule that lets nobody in but the panel's addresses, for the command that repairs a
/// port left open: the lines the admin pastes.
pub fn fix_commands(port: u16, ips: &[IpAddr]) -> Vec<String> {
    let mut out = vec![format!("ufw delete allow {port}/tcp")];
    if ips.is_empty() {
        out.push(format!("# first: the panel's address, as data/node/{FILE} has it"));
        out.push(format!("ufw allow from <panel-ip> to any port {port} proto tcp"));
    } else {
        out.extend(ips.iter().map(|ip| format!("ufw allow {}", host::from_rule(*ip, port))));
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    fn tmpdir(name: &str) -> std::path::PathBuf {
        let d = std::env::temp_dir().join(format!("paneladdr-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&d);
        fs::create_dir_all(d.join("data/node")).unwrap();
        d
    }

    #[test]
    fn what_the_node_wrote() {
        let d = tmpdir("read");
        let file = d.join("data/node").join(FILE);
        fs::write(
            &file,
            r#"{"ips":[{"ip":"203.0.113.7","last":"2026-10-16T12:00:00Z"},{"ip":"2001:db8::1","last":"2026-10-16T11:59:00Z"}]}"#,
        )
        .unwrap();
        let seen = read(&d).expect("the file is read");
        let ip = |s: &str| -> IpAddr { s.parse().unwrap() };
        assert_eq!(seen.addrs(), vec![ip("203.0.113.7"), ip("2001:db8::1")]);
        // The times the node writes are Go's RFC 3339 with a fraction.
        fs::write(&file, r#"{"ips":[{"ip":"203.0.113.7","last":"2026-10-16T12:00:00.481512344Z"}]}"#).unwrap();
        assert_eq!(read(&d).and_then(|s| s.last()), clock::parse_rfc3339("2026-10-16T12:00:00Z"));
        assert!(seen.last().is_some_and(|t| clock::parse_rfc3339("2026-10-16T12:00:00Z") == Some(t)));
        // An empty or broken file is a node whose panel has not been seen, not an error.
        fs::write(&file, r#"{"ips":[]}"#).unwrap();
        assert!(read(&d).is_none());
        fs::write(&file, "not json").unwrap();
        assert!(read(&d).is_none());
        assert!(forget(&d));
        assert!(read(&d).is_none());
        assert!(!file.exists());
        fs::remove_dir_all(&d).unwrap();
    }

    // The repair command names the panel's address when it is known, and says where to find
    // it when it is not.
    #[test]
    fn the_repair_command() {
        let ip = |s: &str| -> IpAddr { s.parse().unwrap() };
        let cmds = fix_commands(25305, &[ip("203.0.113.7")]);
        assert_eq!(
            cmds,
            vec!["ufw delete allow 25305/tcp".to_string(), "ufw allow from 203.0.113.7 to any port 25305 proto tcp".to_string()]
        );
        let cmds = fix_commands(25305, &[]);
        assert_eq!(cmds.len(), 3);
        assert!(cmds[2].contains("proto tcp") && cmds[1].contains(FILE));
    }

    // The file decides who is wanted: an address newer than the moment the rules changed
    // means the panel got through; a stale one means it did not.
    #[test]
    fn a_connection_after_a_change_counts() {
        let d = tmpdir("came");
        let file = d.join("data/node").join(FILE);
        let at = |s: &str| clock::parse_rfc3339(s).unwrap();
        fs::write(&file, r#"{"ips":[{"ip":"203.0.113.7","last":"2026-10-16T12:00:00Z"}]}"#).unwrap();
        let read = || read(&d);
        assert!(came_back_at(read, at("2026-10-16T11:59:00Z"), Duration::from_secs(0)));
        assert!(!came_back_at(read, at("2026-10-16T12:01:00Z"), Duration::from_secs(0)));
        fs::remove_dir_all(&d).unwrap();
    }
}
