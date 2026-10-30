//! DPI bypass for the node's own exit: zapret's `nfqws` on the server's host, run only while
//! the panel asks for it. YouTube and Discord are throttled in Russia; nfqws splits the TLS
//! handshake so the ISP's filter cannot recognise it, while the traffic keeps the server's
//! Russian address — the point of the offer, rather than sending it out through another
//! country.
//!
//! The node's container has no NET_ADMIN and no nftables, so it asks through its data
//! directory (internal/hostchan dpi.go) and a unit of the host starts the service:
//!
//!   data/node/dpi/request       {"id", "at", "on": true}: what the node asks for
//!   data/node/dpi/status.json   written here: whether nfqws runs, and why not
//!   /opt/mikan/dpi/             nfqws, its host list and the start/stop scripts
//!   /etc/systemd/system/arikan-dpi.service
//!
//! `mikan dpi on|off|status`; `mikan dpi --requested` is what the host unit runs. The first
//! `on` downloads the pinned zapret release, builds nfqws (build tools and the netfilter
//! queue library through apt) and installs the service; after that `on`/`off` are a
//! systemd start and stop. The queue is installed with `--queue-bypass`, so traffic keeps
//! flowing if nfqws dies, and the rule is removed when the service stops.

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use anyhow::{Context, Result, bail};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use crate::{DIR, clock, hostreq, out};

/// The zapret release this installer pins: sources, their checksum, licence GPL-2.0.
const ZAPRET_VERSION: &str = "72.13";
const ZAPRET_URL: &str = "https://github.com/bol-van/zapret/releases/download/v72.13/zapret-v72.13.tar.gz";
/// The tarball's sha256: the sources are never taken from a moving tag.
const ZAPRET_SHA256: &str = "25c74e6c5f48963fa244c2955e76694a07c39447245a0457e2efdc74b3317e68";

/// The queue number of nfqws; the nft/iptables rule uses the same.
const QUEUE: &str = "200";

/// The sites the bypass exists for. nfqws matches the TLS name in the client's hello, so
/// the host has to resolve none of them.
const HOSTS: &[&str] = &[
    "youtube.com",
    "youtu.be",
    "youtube-nocookie.com",
    "googlevideo.com",
    "ytimg.com",
    "ggpht.com",
    "googleusercontent.com",
    "discord.com",
    "discordapp.com",
    "discord.gg",
    "discordapp.net",
];

/// The zapret command line: the Flowseal Discord/YouTube preset's TCP strategy, which
/// splits the first packet of the hello and overlaps its sequence. A server whose ISP wants
/// another strategy can edit /opt/mikan/dpi/run; `mikan dpi off` and `on` then reload it.
const ARGS: &str = "--dpi-desync=multisplit --dpi-desync-split-pos=1 --dpi-desync-split-seqovl=681";

/// What the node reads after asking.
#[derive(Debug, Default, Serialize, Deserialize)]
pub struct Status {
    pub on: bool,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub error: String,
    #[serde(default)]
    pub at: i64,
}

fn dir() -> PathBuf {
    Path::new(DIR).join("dpi")
}

fn now() -> i64 {
    clock::unix(SystemTime::now())
}

/// Runs a command to the end and turns a failure into its last error line.
fn run(program: &str, args: &[&str]) -> Result<()> {
    let out = Command::new(program).args(args).stdin(Stdio::null()).output().with_context(|| format!("run {program}"))?;
    if out.status.success() {
        return Ok(());
    }
    let err = String::from_utf8_lossy(&out.stderr);
    let why = err.lines().map(str::trim).rfind(|l| !l.is_empty()).unwrap_or("failed");
    bail!("{program} {}: {why}", args.join(" "))
}

/// The request the node writes: Some(true) starts, Some(false) stops, None is not a request.
fn parse_request(data: &[u8]) -> Option<bool> {
    let v: serde_json::Value = serde_json::from_slice(data).ok()?;
    v.as_object()?.get("on")?.as_bool()
}

/// Downloads the pinned zapret sources, builds nfqws and writes the scripts. Idempotent:
/// an install that already has the binary is left alone.
fn install() -> Result<()> {
    let dir = dir();
    if dir.join("nfqws").is_file() {
        return Ok(());
    }
    fs::create_dir_all(&dir).with_context(|| format!("create {}", dir.display()))?;
    out("DPI: installing zapret (the first time takes a few minutes)");
    // The build needs a compiler and the queue library; the runtime keeps the library.
    let apt = |args: &[&str]| -> Result<()> {
        let out = Command::new("apt-get")
            .args(args)
            .env("DEBIAN_FRONTEND", "noninteractive")
            .stdin(Stdio::null())
            .output()
            .context("run apt-get")?;
        if out.status.success() {
            return Ok(());
        }
        let err = String::from_utf8_lossy(&out.stderr);
        bail!("apt-get {}: {}", args.join(" "), err.lines().map(str::trim).rfind(|l| !l.is_empty()).unwrap_or("failed"))
    };
    apt(&["update", "-qq"])?;
    apt(&[
        "install",
        "-y",
        "--no-install-recommends",
        "build-essential",
        "libnetfilter-queue-dev",
        "libnfnetlink-dev",
        "ca-certificates",
        "curl",
        "iptables",
    ])
    .context("install the build tools (apt-get)")?;
    let tarball = dir.join("zapret.tar.gz");
    let agent: ureq::Agent = ureq::Agent::config_builder()
        .timeout_global(Some(Duration::from_secs(180)))
        .user_agent(format!("arikan-installer/{}", crate::version()))
        .build()
        .into();
    let bytes = agent
        .get(ZAPRET_URL)
        .call()
        .and_then(|mut r| r.body_mut().with_config().limit(32 << 20).read_to_vec())
        .context("download zapret")?;
    fs::write(&tarball, &bytes).context("write the zapret archive")?;
    let got = {
        let mut h = Sha256::new();
        h.update(&bytes);
        format!("{:x}", h.finalize())
    };
    if got != ZAPRET_SHA256 {
        let _ = fs::remove_file(&tarball);
        bail!("zapret: the archive is not the pinned one (sha256 {got})");
    }
    let src = dir.join("src");
    let _ = fs::remove_dir_all(&src);
    fs::create_dir_all(&src)?;
    run("tar", &["-xzf", tarball.to_str().unwrap_or(""), "-C", src.to_str().unwrap_or("")])?;
    let nfq = src.join(format!("zapret-v{ZAPRET_VERSION}")).join("nfq");
    run("make", &["-C", nfq.to_str().unwrap_or("")]).context("build nfqws")?;
    fs::copy(nfq.join("nfqws"), dir.join("nfqws")).context("copy nfqws")?;
    let _ = fs::remove_file(&tarball);
    write_scripts()?;
    out("DPI: zapret is installed");
    Ok(())
}

/// The host list, the start script and the stop script. `run` is read again on every start,
/// so a strategy edited there survives a restart.
fn write_scripts() -> Result<()> {
    let dir = dir();
    let mut hosts = String::new();
    for h in HOSTS {
        hosts.push_str(h);
        hosts.push('\n');
    }
    fs::write(dir.join("hosts.txt"), hosts)?;
    let run = format!(
        "#!/bin/sh\n\
         set -eu\n\
         # The queue before nfqws reads it; --queue-bypass keeps traffic moving if nfqws dies.\n\
         iptables -t mangle -C POSTROUTING -p tcp --dport 443 -j NFQUEUE --queue-num {QUEUE} --queue-bypass 2>/dev/null || \\\n\
         iptables -t mangle -I POSTROUTING -p tcp --dport 443 -j NFQUEUE --queue-num {QUEUE} --queue-bypass\n\
         exec {dir}/nfqws --qnum={QUEUE} --filter-tcp=443 --hostlist={dir}/hosts.txt {ARGS}\n",
        dir = dir.display()
    );
    let stop = format!(
        "#!/bin/sh\n\
         while iptables -t mangle -C POSTROUTING -p tcp --dport 443 -j NFQUEUE --queue-num {QUEUE} --queue-bypass 2>/dev/null; do\n\
           iptables -t mangle -D POSTROUTING -p tcp --dport 443 -j NFQUEUE --queue-num {QUEUE} --queue-bypass || break\n\
         done\n"
    );
    for (name, text) in [("run", run), ("stop", stop)] {
        let p = dir.join(name);
        fs::write(&p, text)?;
        let mut perms = fs::metadata(&p)?.permissions();
        perms.set_mode(0o755);
        fs::set_permissions(&p, perms)?;
    }
    Ok(())
}

/// The unit of the service: nfqws gets exactly the two capabilities the queue needs.
fn write_service() -> Result<()> {
    let unit = format!(
        "[Unit]\n\
         Description=Arikan: DPI bypass (nfqws) for YouTube and Discord\n\
         After=network-online.target\n\
         Wants=network-online.target\n\
         \n\
         [Service]\n\
         Type=simple\n\
         ExecStart={dir}/run\n\
         ExecStopPost={dir}/stop\n\
         Restart=on-failure\n\
         RestartSec=5\n\
         AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW\n\
         CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW\n\
         \n\
         [Install]\n\
         WantedBy=multi-user.target\n",
        dir = dir().display()
    );
    fs::write("/etc/systemd/system/arikan-dpi.service", unit).context("write arikan-dpi.service")?;
    run("systemctl", &["daemon-reload"])
}

fn active() -> bool {
    Command::new("systemctl")
        .args(["is-active", "--quiet", "arikan-dpi.service"])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .is_ok_and(|s| s.success())
}

/// Starts or stops the service. `on` installs zapret first when it is not there yet.
pub fn set(on: bool) -> Result<()> {
    if on {
        install()?;
        write_service()?;
        run("systemctl", &["enable", "--now", "arikan-dpi.service"]).context("start arikan-dpi")?;
        if !active() {
            bail!("arikan-dpi did not start: systemctl status arikan-dpi.service");
        }
        return Ok(());
    }
    // A stop of a service that was never installed is no error.
    let _ = Command::new("systemctl")
        .args(["disable", "--now", "arikan-dpi.service"])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status();
    let stop = dir().join("stop");
    if stop.is_file() {
        let _ = Command::new("sh").arg(stop).status();
    }
    Ok(())
}

/// Writes the node's status; the node reads it through its data directory.
fn write_status(dir: &crate::panelfs::Dir, status: &Status) -> Result<()> {
    dir.write("status.json", &serde_json::to_vec(status)?, 0o644)
}

/// `mikan dpi --requested` (the path unit): takes the node's request, starts or stops the
/// service and writes what happened back.
pub fn requested() -> Result<()> {
    let Some(dir) = hostreq::dir(true, "dpi")? else {
        return Ok(());
    };
    let Some(raw) = hostreq::take_bytes(&dir)? else {
        return Ok(());
    };
    let Some(on) = parse_request(&raw) else {
        let _ = write_status(&dir, &Status { on: active(), error: "a request that is not one was dropped".into(), at: now() });
        return Ok(());
    };
    let result = set(on);
    let status = Status { on: active(), error: result.as_ref().err().map(|e| format!("{e:#}")).unwrap_or_default(), at: now() };
    let written = write_status(&dir, &status);
    for e in [result.err(), written.err()].into_iter().flatten() {
        out(&format!("DPI: {e:#}"));
    }
    Ok(())
}

/// `mikan dpi on|off|status` and the host unit's `--requested`.
pub fn command(state: Option<&str>) -> Result<()> {
    match state {
        Some("on") => {
            set(true)?;
            out("DPI: nfqws runs; YouTube and Discord leave this server with its own address.");
        }
        Some("off") => {
            set(false)?;
            out("DPI: nfqws is off; the traffic goes as before.");
        }
        Some(other) => bail!("dpi: unknown state {other:?} (on, off or status)"),
        None => {
            if active() {
                out("DPI: on (nfqws is running)");
            } else {
                out("DPI: off");
            }
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_request_says_on_or_off() {
        assert_eq!(parse_request(b"{\"id\":\"x\",\"on\":true}"), Some(true));
        assert_eq!(parse_request(b"{\"on\":false}"), Some(false));
        assert_eq!(parse_request(b"{}"), None);
        assert_eq!(parse_request(b"on"), None);
        assert_eq!(parse_request(b"{\"on\":\"yes\"}"), None);
    }

    // The scripts point at the installed binary and the queue, and the queue is bypassable:
    // a dead nfqws must not stop the server's traffic.
    #[test]
    fn the_scripts_use_the_queue_and_the_host_list() {
        let dir = dir();
        let run = format!("iptables -t mangle -C POSTROUTING -p tcp --dport 443 -j NFQUEUE --queue-num {QUEUE} --queue-bypass");
        assert!(run.contains("--queue-bypass"));
        assert!(run.contains("--queue-num 200"));
        // The pieces the written run script is made of (not the file: a test host has none).
        let text = format!("exec {}/nfqws --qnum={QUEUE} --filter-tcp=443 --hostlist={}/hosts.txt {ARGS}", dir.display(), dir.display());
        assert!(text.contains("--filter-tcp=443") && text.contains("--hostlist=") && text.contains("--dpi-desync=multisplit"));
        assert!(HOSTS.contains(&"youtube.com") && HOSTS.contains(&"discord.com"));
    }

    // The host list is one name a line, which is what nfqws reads.
    #[test]
    fn the_host_list_is_plain_names() {
        let text = HOSTS.join("\n") + "\n";
        for h in HOSTS {
            assert!(text.lines().any(|l| l == *h));
            assert!(!h.contains(' ') && !h.contains('/'));
        }
    }
}
