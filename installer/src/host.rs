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

/// A node's API port for its panel's address only.
pub fn allow_from(ip: std::net::IpAddr, port: u16) -> Result<()> {
    let ip = ip.to_string();
    let port = port.to_string();
    ufw(&["allow", "from", &ip, "to", "any", "port", &port, "proto", "tcp"])
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

const UNIT_NAMES: [&str; 4] =
    ["arikan-update.service", "arikan-update.timer", "arikan-update-request.service", "arikan-update-request.path"];

/// The unit files of a panel or of a node: the same four, except that the path unit watches
/// the request of the one the server runs (the panel's Update button, or the panel asking
/// a node over its API: data/panel/update/request and data/node/update/request).
fn units(panel: bool) -> [(&'static str, String); 4] {
    let (dir, from) = if panel { ("panel", "the panel") } else { ("node", "the panel this node belongs to") };
    [
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
    ]
}

/// Whether the unit files on disk are the ones this installer writes for a panel or a node
/// (a server updated by an older command has the older ones; a node before 0.5.0.2 has no
/// request unit at all).
pub fn units_current(panel: bool) -> bool {
    units_current_in(Path::new("/etc/systemd/system"), panel)
}

fn units_current_in(dir: &Path, panel: bool) -> bool {
    units(panel).iter().all(|(name, text)| fs::read_to_string(dir.join(name)).is_ok_and(|have| have == *text))
}

/// The update timer and the watch on the update request: a panel's (its Update button) or a
/// node's (the panel asking over the node API). A server that ran stock mikan has the stock
/// ones as well, running the stock command: they are retired here, so what is left after
/// this is this fork's updater and nothing that looks upstream.
pub fn install_units(panel: bool) -> Result<()> {
    if !Path::new("/run/systemd/system").exists() {
        bail!("no systemd");
    }
    for (name, text) in units(panel) {
        fs::write(format!("/etc/systemd/system/{name}"), text)?;
    }
    systemctl(&["daemon-reload"])?;
    systemctl(&["enable", "--now", "arikan-update.timer"])?;
    systemctl(&["enable", "--now", "arikan-update-request.path"])?;
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

pub fn remove_units() {
    let _ = systemctl(&["disable", "--now", "arikan-update.timer", "arikan-update-request.path"]);
    for name in UNIT_NAMES {
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

    // A panel's path unit watches the panel's request, a node's the node's: the same four
    // units, the path unit apart.
    #[test]
    fn the_request_unit_watches_the_directory_of_what_the_server_runs() {
        let (panel, node) = (units(true), units(false));
        let path = |all: &[(&str, String); 4]| all.iter().find(|(n, _)| *n == "arikan-update-request.path").unwrap().1.clone();
        assert!(path(&panel).contains("\nPathExists=/opt/mikan/data/panel/update/request\n"));
        assert!(path(&node).contains("\nPathExists=/opt/mikan/data/node/update/request\n"));
        assert!(!path(&panel).contains("data/node") && !path(&node).contains("data/panel"));
        for ((pn, pt), (nn, nt)) in panel.iter().zip(&node) {
            assert_eq!(pn, nn);
            assert!(UNIT_NAMES.contains(pn), "{pn}");
            if !pn.ends_with(".path") {
                assert_eq!(pt, nt, "{pn} is the same on a panel and on a node");
            }
        }
        // both run the same command, which finds out by itself whose request it is
        let service = &node.iter().find(|(n, _)| *n == "arikan-update-request.service").unwrap().1;
        assert!(service.contains("ExecStart=/usr/local/bin/arikan update --requested\n"));
        assert!(path(&node).contains("Unit=arikan-update-request.service"));
    }

    // A server updated by an older command has the older units: a node with none of the
    // request unit, a panel's file on a node, are not current; each mode's own are.
    #[test]
    fn units_are_current_for_the_mode_they_were_written_for() {
        let d = tmpdir("units");
        assert!(!units_current_in(&d, true) && !units_current_in(&d, false), "nothing written yet");
        let write = |panel: bool| {
            for (name, text) in units(panel) {
                fs::write(d.join(name), text).unwrap();
            }
        };
        write(false);
        assert!(units_current_in(&d, false));
        assert!(!units_current_in(&d, true), "a node's units are not a panel's");
        write(true);
        assert!(units_current_in(&d, true) && !units_current_in(&d, false));
        // a node of 0.5.0.1: the timer and the services, no path unit
        write(false);
        fs::remove_file(d.join("arikan-update-request.path")).unwrap();
        assert!(!units_current_in(&d, false), "a node without the request unit gets it on its next update");
        // an older text of the path unit
        write(false);
        let old = fs::read_to_string(d.join("arikan-update-request.path")).unwrap().replace("data/node/", "data/panel/");
        fs::write(d.join("arikan-update-request.path"), old).unwrap();
        assert!(!units_current_in(&d, false));
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
}
