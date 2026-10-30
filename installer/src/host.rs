//! What Arikan changes on the host besides /opt/mikan: kernel tuning, ufw rules, the
//! update timer and the Arikan command itself.

use std::fs;
use std::path::Path;
use std::process::{Command, Stdio};

use anyhow::{Context, Result, bail};

use crate::system;

pub const SYSCTL: &str = "/etc/sysctl.d/99-arikan.conf";

const SYSCTL_CONF: &str = "# Arikan: BBR for the TCP protocols, bigger UDP buffers for Hysteria2 and TUIC (QUIC)
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr
net.core.rmem_max = 16777216
net.core.wmem_max = 16777216
";

pub fn tune() -> Result<()> {
    fs::write(SYSCTL, SYSCTL_CONF)?;
    crate::system::run_quiet("sysctl", &["--system"])
}

/// HTTPS ports the panel moves a blocked inbound to on its own (internal/panel/autotune
/// Pool): the node cannot open them in the firewall itself.
pub const POOL: [u16; 15] = [2053, 2083, 2087, 2096, 2443, 3443, 4443, 5443, 6443, 7443, 8443, 9443, 10443, 11443, 12443];

/// The ufw rules of an install: the panel's port (or a node's API port), 80 for the
/// certificate authority's HTTP-01 check, 443 and the pool over TCP and UDP. A node needs 80
/// too: the panel orders its public certificate, and the CA comes to the node's port 80.
pub fn rules(panel_port: Option<u16>, node_api: Option<u16>) -> Vec<String> {
    let mut r = Vec::new();
    if let Some(p) = panel_port {
        r.push(format!("{p}/tcp"));
    }
    if let Some(p) = node_api {
        r.push(format!("{p}/tcp"));
    }
    r.push(ACME_RULE.into());
    r.extend(pool_rules());
    r
}

/// Port 80, where the certificate authority checks the server (HTTP-01).
pub const ACME_RULE: &str = "80/tcp";

/// What `arikan update` opens again on a server with ufw on: port 80 (nodes installed before
/// they got public certificates lack it) and the pool, which may have grown.
pub fn update_rules() -> Vec<String> {
    std::iter::once(ACME_RULE.to_string()).chain(pool_rules()).collect()
}

/// 443 and the pool over TCP and UDP: what the protocols and the panel's moves need open.
pub fn pool_rules() -> Vec<String> {
    std::iter::once(443).chain(POOL).flat_map(|p| [format!("{p}/tcp"), format!("{p}/udp")]).collect()
}

/// Opens the rules when ufw is on; returns whether it was.
pub fn open(rules: &[String]) -> Result<bool> {
    if !system::ufw_active() {
        return Ok(false);
    }
    for rule in rules {
        allow(rule)?;
    }
    Ok(true)
}

/// Removes the rules this install opened, when ufw is on: a full uninstall leaves no port
/// open behind it. A rule ufw does not have is not an error — an install may have taken one
/// away by hand, and the purge must still finish.
pub fn close(rules: &[String]) -> Result<bool> {
    if !system::ufw_active() {
        return Ok(false);
    }
    for rule in rules {
        let _ = delete_spec(rule);
    }
    Ok(true)
}

/// Whether a rule is a port or a range of ports over tcp or udp, like 8443/tcp or
/// 20000:20010/udp: all `ufw allow` takes from here, so that nothing a container printed
/// can become an option or another command.
pub fn valid_rule(rule: &str) -> bool {
    let Some((ports, proto)) = rule.split_once('/') else { return false };
    if proto != "tcp" && proto != "udp" {
        return false;
    }
    let port = |p: &str| p.len() <= 5 && p.bytes().all(|b| b.is_ascii_digit()) && p.parse::<u32>().is_ok_and(|n| (1..=65535).contains(&n));
    match ports.split_once(':') {
        Some((a, b)) => port(a) && port(b) && a.parse::<u32>().unwrap_or(0) <= b.parse::<u32>().unwrap_or(0),
        None => port(ports),
    }
}

pub fn allow(rule: &str) -> Result<()> {
    if !valid_rule(rule) {
        bail!("{rule:?} is not a firewall rule like 8443/tcp");
    }
    ufw(&["allow", rule])
}

/// Removes a rule by its spec (`ufw --force delete allow <spec>`). The specs here are always
/// built from an address and a port, never from anything a container printed.
pub fn delete_spec(spec: &str) -> Result<()> {
    let args: Vec<&str> = ["--force", "delete", "allow"].into_iter().chain(spec.split_whitespace()).collect();
    ufw(&args)
}

/// A node's API port for its panel's address only.
pub fn allow_from(ip: std::net::IpAddr, port: u16) -> Result<()> {
    let spec = from_rule(ip, port);
    let args: Vec<&str> = std::iter::once("allow").chain(spec.split_whitespace()).collect();
    ufw(&args)
}

/// The rule that lets one address reach a port: what ufw writes and deletes by.
pub fn from_rule(ip: std::net::IpAddr, port: u16) -> String {
    format!("from {ip} to any port {port} proto tcp")
}

/// Who `ufw status` says may reach `port` over TCP: None for a rule that lets everyone in,
/// the address otherwise. Only the shapes this installer writes are read, `<port>/tcp` and
/// `allow from <address> to any port <port> proto tcp`; both appear with `<port>/tcp` in the
/// To column. A rule without a protocol, or a range that merely covers the port, is not
/// counted: what it lets in beyond the port is the admin's own decision.
pub fn port_rules(status: &str, port: u16) -> Vec<Option<std::net::IpAddr>> {
    let spec = format!("{port}/tcp");
    let mut out = Vec::new();
    for line in status.lines() {
        let words: Vec<&str> = line.split_whitespace().collect();
        let Some(at) = words.iter().position(|w| matches!(*w, "ALLOW" | "LIMIT")) else { continue };
        if at == 0 || !words[..at].contains(&spec.as_str()) {
            continue;
        }
        let from = words.get(at + 1).copied().unwrap_or("");
        if from.eq_ignore_ascii_case("anywhere") || from.eq_ignore_ascii_case("any") {
            out.push(None);
        } else if let Ok(ip) = from.parse() {
            out.push(Some(ip));
        }
    }
    out
}

/// What `ufw` must change to leave a port reachable from `wanted` and nobody else.
#[derive(Debug, Clone, PartialEq, Default)]
pub struct PortPlan {
    /// Rule specs for `ufw delete allow`.
    pub delete: Vec<String>,
    /// Rule specs for `ufw allow`.
    pub add: Vec<String>,
}

impl PortPlan {
    pub fn nothing(&self) -> bool {
        self.delete.is_empty() && self.add.is_empty()
    }
}

/// The plan that puts `port` behind `wanted`: every wanted address that is not allowed yet
/// comes, and every other rule for the port goes (the rule for everyone first). An address
/// in both lists at once: it stays or is added, and whatever else was there goes.
pub fn port_plan(status: &str, port: u16, wanted: &[std::net::IpAddr]) -> PortPlan {
    let have = port_rules(status, port);
    let mut plan = PortPlan::default();
    for ip in wanted {
        if !have.contains(&Some(*ip)) {
            plan.add.push(from_rule(*ip, port));
        }
    }
    for rule in &have {
        match rule {
            None => plan.delete.push(format!("{port}/tcp")),
            Some(ip) if !wanted.contains(ip) => plan.delete.push(from_rule(*ip, port)),
            _ => {}
        }
    }
    plan
}

/// Runs a plan. What is added goes first: the scoped rules are in place before the one for
/// everyone goes, so the change itself never cuts anybody off — and running the same plan
/// twice changes nothing (adding a rule ufw has, or deleting one it does not, is no error).
pub fn apply_plan(plan: &PortPlan) -> Result<()> {
    for spec in &plan.add {
        let args: Vec<&str> = std::iter::once("allow").chain(spec.split_whitespace()).collect();
        ufw(&args)?;
    }
    for spec in &plan.delete {
        delete_spec(spec)?;
    }
    Ok(())
}

fn ufw(args: &[&str]) -> Result<()> {
    crate::system::run_quiet("ufw", args)
}

pub const BIN: &str = "/usr/local/bin/arikan";

/// The command and the units stock mikan installs: a server that moves to this fork keeps
/// them unless they are taken over, and both are a hazard then. The daily timer and the
/// panel's Update button run /usr/local/bin/mikan, and that command fetches the releases of
/// Miroshka000/mikan — the day upstream publishes above this fork, stock mikan is pulled
/// over it. Both are this fork's own with another name (a test keeps them such).
pub const STOCK_BIN: &str = "/usr/local/bin/mikan";
/// Where the stock command is kept when this fork takes its name over. Kept, not deleted:
/// it is not ours to throw away, and a rollback (`install-fork.sh --rollback`) wants it.
pub const STOCK_KEPT: &str = "/usr/local/bin/mikan.stock";
/// The stock units that start the stock updater: the timer that fires nightly and the watch
/// on the update request the panel's button (or a node's panel) leaves.
const STOCK_ACTIVATORS: [&str; 2] = ["mikan-update.timer", "mikan-update-request.path"];

/// Puts this binary at /usr/local/bin/arikan, the server's command, unless it runs from there.
pub fn install_self() -> Result<()> {
    let me = std::env::current_exe()?;
    if fs::canonicalize(&me).ok() == fs::canonicalize(BIN).ok() {
        return Ok(());
    }
    replace_bin(&fs::read(&me)?, None)
}

/// Replaces /usr/local/bin/arikan at once; the running process keeps its old copy. The new
/// file is on the disk, not only in the page cache (a power cut must not leave the command,
/// which both update units run, empty), and it runs and reports the release it is meant to
/// be before it takes the working one's place.
pub fn replace_bin(data: &[u8], release: Option<&str>) -> Result<()> {
    replace_at(Path::new(BIN), data, release)
}

fn replace_at(bin: &Path, data: &[u8], release: Option<&str>) -> Result<()> {
    use std::io::Write;
    use std::os::unix::fs::OpenOptionsExt;
    let mut tmp = bin.as_os_str().to_owned();
    tmp.push(".new");
    let tmp = std::path::PathBuf::from(tmp);
    let _ = fs::remove_file(&tmp);
    let mut f =
        fs::OpenOptions::new().write(true).create_new(true).mode(0o755).open(&tmp).with_context(|| format!("create {}", tmp.display()))?;
    let checked = f.write_all(data).and_then(|()| f.sync_all()).map_err(anyhow::Error::from).and_then(|()| {
        drop(f);
        let out = version_of(&tmp)?;
        match release {
            Some(v) if !out.contains(v) => bail!("the downloaded command is {:?}, not release {v}", out.trim()),
            _ => Ok(()),
        }
    });
    if let Err(e) = checked {
        let _ = fs::remove_file(&tmp);
        return Err(e);
    }
    fs::rename(&tmp, bin).with_context(|| format!("replace {}", bin.display()))?;
    if let Some(dir) = bin.parent().and_then(|d| fs::File::open(d).ok()) {
        let _ = dir.sync_all();
    }
    Ok(())
}

/// What the command at path says to --version; it must run and succeed.
fn version_of(path: &Path) -> Result<String> {
    let mut last = None;
    for _ in 0..5 {
        match Command::new(path).arg("--version").stdin(Stdio::null()).output() {
            Ok(o) if o.status.success() => return Ok(String::from_utf8_lossy(&o.stdout).into_owned()),
            Ok(o) => bail!("the downloaded command does not run ({})", o.status),
            // A file only just written can still be busy for a moment when another thread forked.
            Err(e) if e.kind() == std::io::ErrorKind::ExecutableFileBusy => {
                last = Some(e);
                std::thread::sleep(std::time::Duration::from_millis(100));
            }
            Err(e) => return Err(e).context("run the downloaded command"),
        }
    }
    Err(last.map_or_else(|| anyhow::anyhow!("the downloaded command does not run"), Into::into))
}

const UNIT_NAMES: [&str; 12] = [
    "arikan-update.service",
    "arikan-update.timer",
    "arikan-update-request.service",
    "arikan-update-request.path",
    "arikan-doctor-request.service",
    "arikan-doctor-request.path",
    "arikan-restart-request.service",
    "arikan-restart-request.path",
    "arikan-terminal-request.service",
    "arikan-terminal-request.path",
    "arikan-dpi-request.service",
    "arikan-dpi-request.path",
];

/// A node's own units: the firewall rule of its API port (apilock.rs), kept by a path unit on
/// the node's request and a timer that lifts it when the panel stays away.
const NODE_UNITS: [&str; 3] = ["arikan-apilock.service", "arikan-apilock.path", "arikan-apilock.timer"];

/// The node's units that are enabled and started.
const NODE_WATCHES: [&str; 2] = ["arikan-apilock.path", "arikan-apilock.timer"];

/// The path units, which are enabled and started; the timer too.
const WATCHES: [&str; 6] = [
    "arikan-update.timer",
    "arikan-update-request.path",
    "arikan-doctor-request.path",
    "arikan-restart-request.path",
    "arikan-terminal-request.path",
    "arikan-dpi-request.path",
];

/// The unit files of a panel or of a node: the same, except that each path unit watches
/// the request of the one the server runs (the panel's Update button, or the panel asking
/// a node over its API: data/panel/update/request and data/node/update/request; the same
/// for a doctor run and a restart, hostreq.rs).
fn units(panel: bool) -> Vec<(&'static str, String)> {
    let (dir, from) = if panel { ("panel", "the panel") } else { ("node", "the panel this node belongs to") };
    vec![
        (
            UNIT_NAMES[0],
            "[Unit]
Description=Arikan: update when automatic updates are on
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
# A oneshot unit has no time limit by default: an update stuck on a dead connection
# would keep the timer from ever starting it again. Two hours: a database move alone may
# take an hour (DATABASE_LIMIT), with the pull and the backups around it.
TimeoutStartSec=2h
ExecStart=/usr/local/bin/arikan update --auto
"
            .into(),
        ),
        (
            UNIT_NAMES[1],
            "[Unit]
Description=Arikan: look for an update once a day

[Timer]
OnCalendar=*-*-* 03:00:00
RandomizedDelaySec=3h
Persistent=true

[Install]
WantedBy=timers.target
"
            .into(),
        ),
        (
            UNIT_NAMES[2],
            "[Unit]
Description=Arikan: update asked for in the panel
After=docker.service

[Service]
Type=oneshot
TimeoutStartSec=2h
ExecStart=/usr/local/bin/arikan update --requested
"
            .into(),
        ),
        (
            UNIT_NAMES[3],
            format!(
                "[Unit]
Description=Arikan: wait for an update request from {from}

[Path]
PathExists=/opt/mikan/data/{dir}/update/request
Unit=arikan-update-request.service

[Install]
WantedBy=paths.target
"
            ),
        ),
        (
            UNIT_NAMES[4],
            "[Unit]
Description=Arikan: run the server check the panel asked for
After=docker.service

[Service]
Type=oneshot
# The checks that wait for the network end within a minute; a stuck one must not hold the
# unit (and so the next request) for ever.
TimeoutStartSec=5min
ExecStart=/usr/local/bin/arikan doctor --requested
"
            .into(),
        ),
        (
            UNIT_NAMES[5],
            format!(
                "[Unit]
Description=Arikan: wait for a server check request from {from}

[Path]
PathExists=/opt/mikan/data/{dir}/doctor/request
Unit=arikan-doctor-request.service

[Install]
WantedBy=paths.target
"
            ),
        ),
        (
            UNIT_NAMES[6],
            "[Unit]
Description=Arikan: restart the containers as the panel asked
After=docker.service

[Service]
Type=oneshot
# It waits its turn behind an update (the one lock), which may take two hours.
TimeoutStartSec=2h
ExecStart=/usr/local/bin/arikan restart --requested
"
            .into(),
        ),
        (
            UNIT_NAMES[7],
            format!(
                "[Unit]
Description=Arikan: wait for a restart request from {from}

[Path]
PathExists=/opt/mikan/data/{dir}/restart/request
Unit=arikan-restart-request.service

[Install]
WantedBy=paths.target
"
            ),
        ),
        (
            UNIT_NAMES[8],
            "[Unit]
Description=Arikan: one support terminal session, opened by the panel for a live grant

[Service]
Type=simple
# The agent ends the session by itself (the grant's end, the panel letting go, 15 minutes
# of silence). This is the backstop for one that is stuck: no grant lasts longer than 24
# hours. systemd kills the whole cgroup when the agent ends, so a process the shell left
# behind does not outlive the session.
RuntimeMaxSec=25h
TimeoutStartSec=1min
KillMode=control-group
SendSIGKILL=yes
TimeoutStopSec=10s
ExecStart=/usr/local/bin/arikan terminal-agent
"
            .into(),
        ),
        (
            UNIT_NAMES[9],
            format!(
                "[Unit]
Description=Arikan: wait for a support terminal request from {from}

[Path]
PathExists=/opt/mikan/data/{dir}/support/terminal/request
Unit=arikan-terminal-request.service

[Install]
WantedBy=paths.target
"
            ),
        ),
        (
            UNIT_NAMES[10],
            "[Unit]
Description=Arikan: DPI bypass (nfqws) asked for by a node

[Service]
Type=oneshot
# The first ask installs zapret and builds nfqws: minutes, not seconds. A stuck build
# must not hold the next request for ever.
TimeoutStartSec=15min
ExecStart=/usr/local/bin/arikan dpi --requested
"
            .into(),
        ),
        (
            UNIT_NAMES[11],
            // A panel's own node asks through data/node too: the local node lives on the
            // same host, and its data directory is the one the panel's compose gives it.
            "[Unit]
Description=Arikan: wait for a node to ask for the DPI bypass

[Path]
PathExists=/opt/mikan/data/node/dpi/request
Unit=arikan-dpi-request.service

[Install]
WantedBy=paths.target
"
            .into(),
        ),
    ]
}

/// The units of a node alone (NODE_UNITS).
fn node_units() -> Vec<(&'static str, String)> {
    vec![
        (
            NODE_UNITS[0],
            "[Unit]
Description=Arikan: the firewall rule of the node's API port, for its panel alone

[Service]
Type=oneshot
# One nft run and a few small files; a stuck one must not hold the next request.
TimeoutStartSec=1min
ExecStart=/usr/local/bin/arikan api-lockdown --requested
"
            .into(),
        ),
        (
            NODE_UNITS[1],
            "[Unit]
Description=Arikan: wait for the node to ask for the rule of its API port

[Path]
PathExists=/opt/mikan/data/node/apilock/request
Unit=arikan-apilock.service

[Install]
WantedBy=paths.target
"
            .into(),
        ),
        (
            NODE_UNITS[2],
            "[Unit]
Description=Arikan: lift the rule of the node's API port when the panel stays away

[Timer]
OnBootSec=1min
OnUnitActiveSec=1min
AccuracySec=10s
Unit=arikan-apilock.service

[Install]
WantedBy=timers.target
"
            .into(),
        ),
    ]
}

/// Whether the unit files on disk are the ones this installer writes for a panel or a node
/// (a server updated by an older command has the older ones; a node before 0.5.0.2 has no
/// request unit at all).
pub fn units_current(panel: bool) -> bool {
    units_current_in(Path::new("/etc/systemd/system"), panel, terminal_off())
}

/// The support terminal's units, which a server with MIKAN_SUPPORT_ACCESS=off in its settings
/// does not have: nothing there can start a shell for an operator.
const TERMINAL_UNITS: [&str; 2] = ["arikan-terminal-request.service", "arikan-terminal-request.path"];

/// Whether the server's settings turn support access off (MIKAN_SUPPORT_ACCESS=off).
fn terminal_off() -> bool {
    crate::config::Config::load(crate::DIR).is_ok_and(|e| e.get("MIKAN_SUPPORT_ACCESS") == Some("off"))
}

/// The units a server should have: all of them, or all but the terminal's (terminal_off); a
/// node's own on a node only.
fn wanted(panel: bool, terminal_off: bool) -> Vec<(&'static str, String)> {
    let mut all: Vec<_> = units(panel).into_iter().filter(|(name, _)| !(terminal_off && TERMINAL_UNITS.contains(name))).collect();
    if !panel {
        all.extend(node_units());
    }
    all
}

/// The units a server must not have: the terminal's when it is off, a node's on a panel.
fn unwanted(panel: bool, terminal_off: bool) -> Vec<&'static str> {
    let mut out = Vec::new();
    if terminal_off {
        out.extend(TERMINAL_UNITS);
    }
    if panel {
        out.extend(NODE_UNITS);
    }
    out
}

fn units_current_in(dir: &Path, panel: bool, terminal_off: bool) -> bool {
    wanted(panel, terminal_off).iter().all(|(name, text)| fs::read_to_string(dir.join(name)).is_ok_and(|have| have == *text))
        && !unwanted(panel, terminal_off).iter().any(|name| dir.join(name).exists())
}

/// The update timer and the watches on the requests of a panel (its Update button, a server
/// check, a restart) or of a node (the panel asking over the node API). Run again, it
/// leaves a server as it is: the files are written only where they differ, the units
/// that are enabled stay so. The stock mikan updater is retired besides (retire_stock),
/// so what runs on the host after this is this fork's and nothing that looks upstream.
pub fn install_units(panel: bool) -> Result<()> {
    if !Path::new("/run/systemd/system").exists() {
        bail!("no systemd");
    }
    let off = terminal_off();
    if off {
        // Turned off after the units were installed: they go, a session running ends.
        let _ = systemctl(&["disable", "--now", TERMINAL_UNITS[1], TERMINAL_UNITS[0]]);
    }
    if panel && Path::new("/etc/systemd/system").join(NODE_UNITS[0]).exists() {
        // A node's units on what is a panel now: they go before their files.
        let _ = systemctl(&["disable", "--now", NODE_WATCHES[0], NODE_WATCHES[1], NODE_UNITS[0]]);
    }
    write_units(Path::new("/etc/systemd/system"), panel, off)?;
    systemctl(&["daemon-reload"])?;
    let watches = WATCHES.iter().chain(if panel { &[][..] } else { &NODE_WATCHES[..] });
    for name in watches {
        if !(off && TERMINAL_UNITS.contains(name)) {
            systemctl(&["enable", "--now", *name])?;
        }
    }
    retire_stock();
    Ok(())
}

/// Takes the stock mikan updater out of the way. Its units are stopped and disabled (their
/// files stay: nothing of upstream's is deleted, and a rollback turns them on again), its
/// command is kept at STOCK_KEPT, and /usr/local/bin/mikan becomes a link to this fork's
/// command — the same name for the same thing, the way the image links its own two. After
/// this nothing on the host reaches upstream: `mikan update` is `arikan update`, and the
/// nightly timer is this fork's. Errors are ignored: a takeover that cannot be made is not
/// worth failing an install or an update over, and the next one (every update runs this)
/// tries again.
pub fn retire_stock() {
    let mut disable = vec!["disable", "--now"];
    disable.extend(STOCK_ACTIVATORS);
    let _ = systemctl(&disable);
    retire_stock_at(Path::new(STOCK_BIN), Path::new(STOCK_KEPT), Path::new(BIN));
}

/// What retire_stock does with the paths, all of it that can be tested: the stock command
/// (when there is one, and it is not already this fork's own command) moves to kept, and
/// this fork's command takes its name.
///
/// The check that bin is this fork's own command is by its resolved path, not by what the
/// name looks like: releases before the rename (0.5.1.1) run from /usr/local/bin/mikan —
/// their units are mikan-update.* and /usr/local/bin/arikan is a link to them — and an
/// update to a later release replaces that very file. Deleting it as "the stock command"
/// would break the command the server updates itself with.
fn retire_stock_at(bin: &Path, kept: &Path, ours: &Path) {
    if fs::symlink_metadata(bin).is_err() {
        return; // no stock command here (a fork that never had one): none is invented
    }
    // Already this fork's own command: a build from before the rename runs from this very
    // file, and an update replaces it there — moving it aside would break the command the
    // server updates itself with.
    if let (Ok(a), Ok(b)) = (fs::canonicalize(bin), fs::canonicalize(ours))
        && a == b
    {
        return;
    }
    // The stock command is kept once: a second takeover (an update after a migration)
    // finds the older one still there and does not put a fork's own older build in its
    // place — a rollback wants the command a stock server came with.
    if !kept.exists() && fs::rename(bin, kept).is_err() {
        return;
    }
    let _ = fs::remove_file(bin);
    let _ = std::os::unix::fs::symlink(ours, bin);
}

/// The other way, for `arikan uninstall`: the stock command comes back under its own name
/// (the takeover kept it at STOCK_KEPT) and this fork's link goes; true when it did. Its
/// units are left disabled — nothing updates a server whose admin has just uninstalled,
/// and `systemctl enable --now mikan-update.timer mikan-update-request.path` brings the
/// stock updater back for a server returning to stock mikan. A command of its own at
/// /usr/local/bin/mikan (the admin put stock back) is not touched.
pub fn restore_stock() -> bool {
    restore_stock_at(Path::new(STOCK_BIN), Path::new(STOCK_KEPT), Path::new(BIN))
}

fn restore_stock_at(bin: &Path, kept: &Path, ours: &Path) -> bool {
    if !kept.exists() || fs::read_link(bin).ok().as_deref() != Some(ours) {
        return false;
    }
    fs::remove_file(bin).is_ok() && fs::rename(kept, bin).is_ok()
}

/// Writes the unit files into dir where they are not there or differ, and removes the ones
/// the server must not have (unwanted); returns how many it wrote. A second run writes none.
fn write_units(dir: &Path, panel: bool, terminal_off: bool) -> Result<usize> {
    for name in unwanted(panel, terminal_off) {
        match fs::remove_file(dir.join(name)) {
            Err(e) if e.kind() != std::io::ErrorKind::NotFound => return Err(e).with_context(|| format!("remove {name}")),
            _ => {}
        }
    }
    let mut written = 0;
    for (name, text) in wanted(panel, terminal_off) {
        let path = dir.join(name);
        if fs::read_to_string(&path).is_ok_and(|have| have == text) {
            continue;
        }
        fs::write(&path, text).with_context(|| format!("write {}", path.display()))?;
        written += 1;
    }
    Ok(written)
}

pub fn remove_units() {
    let mut disable = vec!["disable", "--now"];
    disable.extend(WATCHES);
    disable.extend(NODE_WATCHES);
    let _ = systemctl(&disable);
    for name in UNIT_NAMES.iter().chain(&NODE_UNITS) {
        let _ = fs::remove_file(format!("/etc/systemd/system/{name}"));
    }
    let _ = systemctl(&["daemon-reload"]);
}

fn systemctl(args: &[&str]) -> Result<()> {
    crate::system::run_quiet("systemctl", args)
}

#[cfg(test)]
mod tests {
    use super::*;

    // The panel moves a blocked inbound and puts a cascade relay on a port of this pool;
    // the installer must have opened them all.
    #[test]
    fn pool_matches_the_panel() {
        let go = fs::read_to_string("../internal/panel/domain/ports.go").unwrap();
        let start = go.find("var PortPool = []int{").expect("domain.PortPool") + "var PortPool = []int{".len();
        let list = &go[start..start + go[start..].find('}').unwrap()];
        let pool: Vec<u16> = list.split(',').map(|p| p.trim().parse().unwrap()).collect();
        assert_eq!(pool, POOL);
    }

    // What a container prints is not a command: only a port (range) and a protocol pass.
    #[test]
    fn only_port_rules_reach_ufw() {
        for ok in ["443/tcp", "8443/udp", "20000:20010/tcp", "1/tcp", "65535/udp"] {
            assert!(valid_rule(ok), "{ok}");
        }
        for bad in [
            "",
            "443",
            "443/icmp",
            "0/tcp",
            "65536/tcp",
            "99999/tcp",
            "-1/tcp",
            "443/tcp;ls",
            "443/tcp --force",
            "--help/tcp",
            "443:/tcp",
            ":443/tcp",
            "20010:20000/tcp",
            "4 43/tcp",
            "443/TCP",
            "443/tcp\n",
            "1e3/tcp",
            "+443/tcp",
            "443/tcp/udp",
        ] {
            assert!(!valid_rule(bad), "{bad:?}");
        }
        assert!(allow("--help").is_err());
    }

    fn tmpdir(name: &str) -> std::path::PathBuf {
        let d = std::env::temp_dir().join(format!("host-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&d);
        fs::create_dir_all(&d).unwrap();
        d
    }

    // The command is replaced only by a file that runs and is the release meant: a
    // truncated or wrong download leaves the working one.
    #[test]
    fn the_command_is_replaced_only_by_a_working_release() {
        use std::os::unix::fs::PermissionsExt;
        let d = tmpdir("bin");
        let bin = d.join("arikan");
        fs::write(&bin, "#!/bin/sh\necho arikan 0.4.3\n").unwrap();
        fs::set_permissions(&bin, fs::Permissions::from_mode(0o755)).unwrap();
        let old = fs::read(&bin).unwrap();
        let new = b"#!/bin/sh\necho arikan 0.4.4\n";
        for (what, data, release) in [
            ("wrong release", &new[..], Some("0.4.5")),
            ("does not run", &b"#!/bin/sh\nexit 3\n"[..], None),
            ("truncated", &new[..8], Some("0.4.4")),
            ("empty", &b""[..], None),
        ] {
            assert!(replace_at(&bin, data, release).is_err(), "{what} replaced the command");
            assert_eq!(fs::read(&bin).unwrap(), old, "{what}");
            assert!(!d.join("Arikan.new").exists(), "{what} left its file");
        }
        replace_at(&bin, new, Some("0.4.4")).unwrap();
        assert_eq!(fs::read(&bin).unwrap(), new);
        assert_eq!(fs::metadata(&bin).unwrap().permissions().mode() & 0o777, 0o755);
        fs::remove_dir_all(&d).unwrap();
    }

    // A oneshot unit has no time limit unless it is given one.
    #[test]
    fn update_units_have_a_time_limit() {
        for panel in [true, false] {
            for (name, text) in units(panel) {
                if name.ends_with(".service") {
                    assert!(text.contains("TimeoutStartSec="), "{name}");
                }
            }
        }
    }

    // A panel's path units watch the panel's requests, a node's the node's: the same units,
    // the path units apart.
    #[test]
    fn the_request_unit_watches_the_directory_of_what_the_server_runs() {
        let (panel, node) = (units(true), units(false));
        assert_eq!(panel.len(), UNIT_NAMES.len());
        let unit = |all: &[(&str, String)], name: &str| all.iter().find(|(n, _)| *n == name).unwrap().1.clone();
        for (kind, command) in [("update", "update --requested"), ("doctor", "doctor --requested"), ("restart", "restart --requested")] {
            let path_unit = format!("arikan-{kind}-request.path");
            let (p, n) = (unit(&panel, &path_unit), unit(&node, &path_unit));
            assert!(p.contains(&format!("\nPathExists=/opt/mikan/data/panel/{kind}/request\n")), "{kind}");
            assert!(n.contains(&format!("\nPathExists=/opt/mikan/data/node/{kind}/request\n")), "{kind}");
            assert!(!p.contains("data/node") && !n.contains("data/panel"), "{kind}");
            // both run the same command, which finds out by itself whose request it is
            let service = unit(&node, &format!("arikan-{kind}-request.service"));
            assert!(service.contains(&format!("ExecStart=/usr/local/bin/arikan {command}\n")), "{kind}");
            assert!(n.contains(&format!("Unit=arikan-{kind}-request.service")), "{kind}");
        }
        for ((pn, pt), (nn, nt)) in panel.iter().zip(&node) {
            assert_eq!(pn, nn);
            assert!(UNIT_NAMES.contains(pn), "{pn}");
            if !pn.ends_with(".path") {
                assert_eq!(pt, nt, "{pn} is the same on a panel and on a node");
            }
        }
        // what is enabled is a unit that exists
        assert!(WATCHES.iter().all(|w| UNIT_NAMES.contains(w)));
    }

    // The terminal's request lies deeper (support/terminal/), the agent is the one fixed
    // command, it runs apart from the others and is enabled like them.
    #[test]
    fn the_terminal_unit_watches_its_own_request_and_runs_the_agent() {
        let (panel, node) = (units(true), units(false));
        let unit = |all: &[(&str, String)], name: &str| all.iter().find(|(n, _)| *n == name).unwrap().1.clone();
        let (p, n) = (unit(&panel, "arikan-terminal-request.path"), unit(&node, "arikan-terminal-request.path"));
        assert!(p.contains("\nPathExists=/opt/mikan/data/panel/support/terminal/request\n") && !p.contains("data/node"));
        assert!(n.contains("\nPathExists=/opt/mikan/data/node/support/terminal/request\n") && !n.contains("data/panel"));
        assert!(p.contains("Unit=arikan-terminal-request.service"));
        let service = unit(&panel, "arikan-terminal-request.service");
        assert_eq!(service, unit(&node, "arikan-terminal-request.service"));
        assert!(service.contains("\nExecStart=/usr/local/bin/arikan terminal-agent\n"));
        // A session is a long-running process whose whole cgroup goes with it, and that is
        // stopped at 25 hours whatever happens (a grant lasts 24 at most).
        for want in ["Type=simple", "KillMode=control-group", "SendSIGKILL=yes", "RuntimeMaxSec=25h"] {
            assert!(service.contains(want), "{want}");
        }
        assert!(WATCHES.contains(&"arikan-terminal-request.path"));
    }

    // The DPI request is a node's, and the panel's own node writes into the same
    // data/node directory: one path unit watches that file on either server.
    #[test]
    fn the_dpi_unit_watches_the_nodes_request() {
        let (panel, node) = (units(true), units(false));
        let unit = |all: &[(&str, String)], name: &str| all.iter().find(|(n, _)| *n == name).unwrap().1.clone();
        for all in [&panel, &node] {
            let path = unit(all, "arikan-dpi-request.path");
            assert!(path.contains("\nPathExists=/opt/mikan/data/node/dpi/request\n"));
            assert!(path.contains("Unit=arikan-dpi-request.service"));
        }
        let service = unit(&node, "arikan-dpi-request.service");
        assert_eq!(service, unit(&panel, "arikan-dpi-request.service"));
        assert!(service.contains("\nExecStart=/usr/local/bin/arikan dpi --requested\n"));
        // The first request installs the tool: the build must not be cut at a minute.
        assert!(service.contains("TimeoutStartSec="));
        assert!(WATCHES.contains(&"arikan-dpi-request.path"));
    }

    // Run again, the install leaves the files alone: a server that is up to date is not
    // rewritten, one with an older unit set (no doctor or restart unit, a changed text) gets
    // what is missing and nothing else.
    #[test]
    fn writing_the_units_is_idempotent() {
        let d = tmpdir("write-units");
        assert_eq!(write_units(&d, true, false).unwrap(), UNIT_NAMES.len());
        assert_eq!(write_units(&d, true, false).unwrap(), 0, "a second run rewrites nothing");
        assert!(units_current_in(&d, true, false));
        // An installer of 0.5.1.0 wrote the update units only.
        for gone in
            ["arikan-doctor-request.service", "arikan-doctor-request.path", "arikan-restart-request.service", "arikan-restart-request.path"]
        {
            fs::remove_file(d.join(gone)).unwrap();
        }
        assert!(!units_current_in(&d, true, false), "an old install is not current: arikan update brings the new units");
        assert_eq!(write_units(&d, true, false).unwrap(), 4);
        assert!(units_current_in(&d, true, false));
        // A changed file is put back, the others stay.
        fs::write(d.join("arikan-doctor-request.path"), "[Path]\nPathExists=/tmp/x\n").unwrap();
        assert_eq!(write_units(&d, true, false).unwrap(), 1);
        assert!(units_current_in(&d, true, false));
        // The same directory for a node: its own watches, the files that are the same stay.
        let before = fs::read_to_string(d.join("arikan-update.timer")).unwrap();
        assert_eq!(
            write_units(&d, false, false).unwrap(),
            4 + NODE_UNITS.len(),
            "only the four path units differ (the DPI one watches data/node on both), and the node's own are new"
        );
        assert_eq!(fs::read_to_string(d.join("arikan-update.timer")).unwrap(), before);
        assert!(fs::read_to_string(d.join("arikan-restart-request.path")).unwrap().contains("data/node/restart/request"));
        assert!(units_current_in(&d, false, false));
        // Back to a panel: the node's own units go.
        write_units(&d, true, false).unwrap();
        assert!(NODE_UNITS.iter().all(|n| !d.join(n).exists()) && units_current_in(&d, true, false));
        fs::remove_dir_all(&d).unwrap();
    }

    // The rule of a node's API port is a node's: its path unit watches the node's request, its
    // timer lifts the rule within a minute of the panel staying away too long, both run the
    // same fixed command and are enabled.
    #[test]
    fn the_api_lock_units_are_a_nodes() {
        let (panel, node) = (wanted(true, false), wanted(false, false));
        assert!(panel.iter().all(|(n, _)| !NODE_UNITS.contains(n)));
        let unit = |name: &str| node.iter().find(|(n, _)| *n == name).unwrap().1.clone();
        assert!(unit("arikan-apilock.service").contains("\nExecStart=/usr/local/bin/arikan api-lockdown --requested\n"));
        assert!(unit("arikan-apilock.service").contains("TimeoutStartSec="));
        assert!(unit("arikan-apilock.path").contains("\nPathExists=/opt/mikan/data/node/apilock/request\nUnit=arikan-apilock.service\n"));
        let timer = unit("arikan-apilock.timer");
        assert!(
            timer.contains("\nOnUnitActiveSec=1min\n")
                && timer.contains("\nOnBootSec=1min\n")
                && timer.contains("Unit=arikan-apilock.service")
        );
        assert!(NODE_WATCHES.iter().all(|w| NODE_UNITS.contains(w)));
        for (name, text) in node_units() {
            assert!(!text.contains("$") && !text.contains("%"), "{name}: nothing is expanded from outside");
        }
    }

    // The units that answer a container run arikan with a flag nothing in a request can change.
    #[test]
    fn the_new_units_run_a_fixed_command() {
        for panel in [true, false] {
            for (name, text) in units(panel) {
                if name.starts_with("arikan-doctor-request")
                    || name.starts_with("arikan-restart-request")
                    || name.starts_with("arikan-terminal-request")
                {
                    assert!(!text.contains("$") && !text.contains("%"), "{name}: nothing is expanded from outside");
                }
            }
        }
    }

    // A server updated by an older command has the older units: a node with none of the
    // request unit, a panel's file on a node, are not current; each mode's own are.
    #[test]
    fn units_are_current_for_the_mode_they_were_written_for() {
        let d = tmpdir("units");
        assert!(!units_current_in(&d, true, false) && !units_current_in(&d, false, false), "nothing written yet");
        let write = |panel: bool| {
            write_units(&d, panel, false).unwrap();
        };
        write(false);
        assert!(units_current_in(&d, false, false));
        assert!(!units_current_in(&d, true, false), "a node's units are not a panel's");
        write(true);
        assert!(units_current_in(&d, true, false) && !units_current_in(&d, false, false));
        // a node of 0.5.0.1: the timer and the services, no path unit
        write(false);
        fs::remove_file(d.join("arikan-update-request.path")).unwrap();
        assert!(!units_current_in(&d, false, false), "a node without the request unit gets it on its next update");
        // an older text of the path unit
        write(false);
        let old = fs::read_to_string(d.join("arikan-update-request.path")).unwrap().replace("data/node/", "data/panel/");
        fs::write(d.join("arikan-update-request.path"), old).unwrap();
        assert!(!units_current_in(&d, false, false));
        fs::remove_dir_all(&d).unwrap();
    }

    // The stock command and units are this fork's own with another name: were upstream to
    // rename one, the takeover would leave a unit behind still running upstream's command.
    #[test]
    fn the_stock_updater_is_this_fork_s_with_another_name() {
        assert_eq!(BIN.replace("arikan", "mikan"), STOCK_BIN);
        let stock: Vec<String> = UNIT_NAMES.iter().map(|u| u.replace("arikan", "mikan")).collect();
        for name in STOCK_ACTIVATORS {
            assert!(stock.iter().any(|s| s == name), "{name} is not one of stock mikan's units");
        }
    }

    // A server that ran stock mikan: its command is kept under the other name and this
    // fork's command takes its name. A link that is already this fork's is left alone, and
    // a second run changes nothing. A fork that never had the stock command invents none.
    #[test]
    fn the_takeover_keeps_the_stock_command_and_takes_its_name() {
        let d = tmpdir("stock");
        let ours = d.join("arikan");
        fs::write(&ours, "#!/bin/sh\necho arikan\n").unwrap();
        let bin = d.join("mikan");
        let kept = d.join("mikan.stock");
        retire_stock_at(&bin, &kept, &ours);
        assert!(!bin.exists() && !kept.exists(), "the name was invented on a server that never had it");
        fs::write(&bin, "#!/bin/sh\necho mikan\n").unwrap();
        retire_stock_at(&bin, &kept, &ours);
        assert_eq!(fs::read_to_string(&kept).unwrap(), "#!/bin/sh\necho mikan\n", "the stock command was lost");
        assert_eq!(fs::read_link(&bin).unwrap(), ours, "mikan does not run this fork's command");
        retire_stock_at(&bin, &kept, &ours);
        assert_eq!(fs::read_link(&bin).unwrap(), ours);
        assert_eq!(fs::read_to_string(&kept).unwrap(), "#!/bin/sh\necho mikan\n");
        fs::remove_dir_all(&d).unwrap();
    }

    // An update after a migration: a release from before the rename runs from
    // /usr/local/bin/mikan and /usr/local/bin/arikan is a link to it — the takeover must
    // leave that very file alone (it is what the server updates itself with) and must not
    // put a fork build where the stock command is kept. Another command under the old name
    // while the stock one is kept: the name becomes this fork's link, what is kept stays.
    #[test]
    fn the_takeover_leaves_this_fork_s_own_command_alone() {
        let d = tmpdir("ours");
        let ours = d.join("arikan");
        let bin = d.join("mikan");
        let kept = d.join("mikan.stock");
        // a release from before the rename: the command lives at the old name, and this
        // fork's name is a link to it
        fs::write(&bin, "fork 0.5.1.1").unwrap();
        fs::write(&kept, "stock").unwrap();
        std::os::unix::fs::symlink(&bin, &ours).unwrap();
        retire_stock_at(&bin, &kept, &ours);
        assert_eq!(fs::read_to_string(&bin).unwrap(), "fork 0.5.1.1", "the running command was moved or removed");
        assert_eq!(fs::read_to_string(&kept).unwrap(), "stock", "a fork build took the stock command's place");
        // A command of someone else's under the old name while the stock one is kept: the old
        // name becomes this fork's link and what is kept stays the stock command.
        fs::remove_file(&ours).unwrap();
        fs::write(&ours, "fork 0.5.1.2").unwrap();
        retire_stock_at(&bin, &kept, &ours);
        assert_eq!(fs::read_link(&bin).unwrap(), ours, "the old name did not become this fork's link");
        assert_eq!(fs::read_to_string(&kept).unwrap(), "stock", "the stock command was replaced");
        fs::remove_dir_all(&d).unwrap();
    }

    // Uninstall: the stock command comes back under its own name and the fork's link goes.
    // A command of its own there (the admin put stock back) is not touched, and with no
    // stock command kept there is nothing to put back.
    #[test]
    fn the_stock_command_comes_back_under_its_name() {
        let d = tmpdir("restore");
        let ours = d.join("arikan");
        fs::write(&ours, "fork").unwrap();
        let bin = d.join("mikan");
        let kept = d.join("mikan.stock");
        assert!(!restore_stock_at(&bin, &kept, &ours), "a stock command appeared from nowhere");
        fs::write(&bin, "stock").unwrap();
        retire_stock_at(&bin, &kept, &ours);
        assert!(restore_stock_at(&bin, &kept, &ours));
        assert_eq!(fs::read_to_string(&bin).unwrap(), "stock");
        assert!(!kept.exists(), "the kept command stays after it was put back");
        assert!(!restore_stock_at(&bin, &kept, &ours), "a command of its own was touched");
        fs::remove_dir_all(&d).unwrap();
    }

    #[test]
    fn firewall_rules() {
        let r = rules(Some(21355), None);
        assert_eq!(&r[..4], ["21355/tcp", "80/tcp", "443/tcp", "443/udp"]);
        assert!(r.contains(&"9443/udp".to_string()));
        assert_eq!(rules(None, Some(25305))[0], "25305/tcp");
        // A node gets 80 for its certificate, also when its API port opens for the panel alone.
        assert!(rules(None, Some(25305)).contains(&"80/tcp".to_string()));
        assert!(rules(None, None).contains(&"80/tcp".to_string()));
        let u = update_rules();
        assert_eq!(u[0], "80/tcp");
        assert!(u.contains(&"12443/udp".to_string()) && u.iter().all(|r| valid_rule(r)));
    }

    /// The rows `ufw status` prints, as a node's server has them.
    const STATUS: &str = "Status: active

To                         Action      From
--                         ------      ----
25305/tcp                  ALLOW       203.0.113.7
25305/tcp                  ALLOW       Anywhere
20000:20100/udp            ALLOW       Anywhere
80/tcp                     ALLOW       Anywhere
25305/tcp                  ALLOW       2001:db8::1
443/tcp                    DENY        Anywhere
80/tcp (v6)                ALLOW       Anywhere (v6)
22/tcp                     LIMIT       198.51.100.9
";

    // Who may reach a port is read from the text, the wide rule included: it is exactly what
    // must be noticed and replaced.
    #[test]
    fn who_may_reach_a_port() {
        let ip = |s: &str| s.parse().unwrap();
        assert_eq!(port_rules(STATUS, 25305), vec![Some(ip("203.0.113.7")), None, Some(ip("2001:db8::1"))]);
        assert_eq!(port_rules(STATUS, 80), vec![None, None]); // the v4 row and the v6 one
        assert_eq!(port_rules(STATUS, 20000), Vec::new()); // a range is not this port
        assert_eq!(port_rules(STATUS, 443), Vec::new()); // a DENY rule lets nobody in
        assert_eq!(port_rules(STATUS, 22), vec![Some(ip("198.51.100.9"))]);
        assert_eq!(port_rules("Status: inactive\n", 25305), Vec::new());
    }

    // The plan that puts the API port behind the panel: the address it already allows stays,
    // the wide rule goes, a wanted address that is missing comes, and a stale one goes.
    #[test]
    fn the_plan_keeping_a_port_for_the_panel() {
        let ip = |s: &str| s.parse().unwrap();
        let plan = port_plan(STATUS, 25305, &[ip("203.0.113.7"), ip("2001:db8::1")]);
        assert_eq!(plan, PortPlan { add: vec![], delete: vec!["25305/tcp".into()] });

        let plan = port_plan(STATUS, 25305, &[ip("203.0.113.7"), ip("203.0.113.8")]);
        assert_eq!(
            plan,
            PortPlan {
                add: vec!["from 203.0.113.8 to any port 25305 proto tcp".into()],
                delete: vec!["25305/tcp".into(), "from 2001:db8::1 to any port 25305 proto tcp".into()],
            }
        );

        // Nothing to do when the port already is exactly what is wanted.
        let only_from = "To                         Action      From\n25305/tcp                  ALLOW       203.0.113.7\n";
        assert!(port_plan(only_from, 25305, &[ip("203.0.113.7")]).nothing());

        // A port with no rule at all: the addresses come, nothing goes.
        let plan = port_plan("Status: active\n", 25305, &[ip("203.0.113.7")]);
        assert_eq!(plan.add, vec!["from 203.0.113.7 to any port 25305 proto tcp".to_string()]);
        assert!(plan.delete.is_empty());

        // Nobody wanted: every rule for the port goes (how the cli closes it).
        let plan = port_plan(STATUS, 25305, &[]);
        assert!(plan.add.is_empty());
        assert_eq!(plan.delete.len(), 3);
        assert!(
            plan.delete.contains(&"25305/tcp".to_string())
                && plan.delete.contains(&"from 203.0.113.7 to any port 25305 proto tcp".to_string())
        );
    }

    // MIKAN_SUPPORT_ACCESS=off: the terminal's units are neither written nor left behind.
    #[test]
    fn support_access_off_keeps_the_terminal_units_away() {
        let d = std::env::temp_dir().join(format!("arikan-units-off-{}", std::process::id()));
        fs::create_dir_all(&d).unwrap();
        write_units(&d, true, false).unwrap();
        assert!(d.join(TERMINAL_UNITS[1]).exists());
        assert!(!units_current_in(&d, true, true), "turned off with the units still there");
        assert_eq!(write_units(&d, true, true).unwrap(), 0, "the other units are the current ones already");
        assert!(TERMINAL_UNITS.iter().all(|n| !d.join(n).exists()));
        assert!(units_current_in(&d, true, true));
        assert!(!units_current_in(&d, true, false), "turned on again: the terminal's units come back");
        assert_eq!(write_units(&d, true, false).unwrap(), 2);
        let _ = fs::remove_dir_all(&d);
    }
}
