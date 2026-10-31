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
//! `on` downloads the pinned zapret release and installs its prebuilt `nfqws` for this
//! machine (a static binary: no compiler, no libcap headers). An architecture the release
//! does not ship is compiled after the headers are installed — `sys/capability.h` comes
//! from libcap, which `build-essential` does not include. After that `on`/`off` are a
//! systemd start and stop. The queue is installed with `--queue-bypass`, so traffic keeps
//! flowing if nfqws dies, and the rule is removed when the service stops.

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{Duration, SystemTime};

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
    "youtubei.googleapis.com",
    "youtube.googleapis.com",
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
    run_env(program, args, &[])
}

fn run_env(program: &str, args: &[&str], env: &[(&str, &str)]) -> Result<()> {
    let mut cmd = Command::new(program);
    cmd.args(args).stdin(Stdio::null());
    for (k, v) in env {
        cmd.env(k, v);
    }
    let out = cmd.output().with_context(|| format!("run {program}"))?;
    if out.status.success() {
        return Ok(());
    }
    // The last line of a compiler or make failure names the target, not the reason: the
    // reason is above it ("cannot find -lz", a missing header). The tail is kept whole, so a
    // failed build says why instead of "Error 1".
    let err = String::from_utf8_lossy(&out.stderr);
    let tail: Vec<&str> = err.lines().map(str::trim).filter(|l| !l.is_empty()).collect();
    if tail.is_empty() {
        bail!("{program} {}: exited with {} and said nothing", args.join(" "), out.status);
    }
    const KEEP: usize = 15;
    let tail = &tail[tail.len().saturating_sub(KEEP)..];
    bail!("{program} {}:\n{}", args.join(" "), tail.join("\n"))
}

/// The request the node writes: Some(true) starts, Some(false) stops, None is not a request.
fn parse_request(data: &[u8]) -> Option<bool> {
    let v: serde_json::Value = serde_json::from_slice(data).ok()?;
    v.as_object()?.get("on")?.as_bool()
}

/// One package manager the host can install iptables and, when nfqws has to be compiled,
/// the headers the build includes (`sys/capability.h` is libcap).
struct Pm {
    cmd: &'static str,
    update: &'static [&'static str],
    install: &'static [&'static str],
    /// What the running service needs. The prebuilt nfqws is static; iptables is not.
    runtime: &'static [&'static str],
    /// Compiler and headers. libcap is not part of build-essential: without it the build
    /// stops at `sec.h: fatal error: sys/capability.h: No such file or directory`.
    build: &'static [&'static str],
}

/// The package lists for a manager the installer knows. None for anything else: the caller
/// says so in plain words instead of running a command that is not there.
fn pm_spec(cmd: &str) -> Option<Pm> {
    match cmd {
        "apt-get" => Some(Pm {
            cmd: "apt-get",
            update: &["update", "-qq"],
            install: &["install", "-y", "--no-install-recommends"],
            runtime: &["iptables"],
            build: &["build-essential", "libcap-dev", "libnetfilter-queue-dev", "libnfnetlink-dev", "zlib1g-dev", "libmnl-dev", "iptables"],
        }),
        "dnf" => Some(rpm_pm("dnf")),
        "yum" => Some(rpm_pm("yum")),
        "apk" => Some(Pm {
            cmd: "apk",
            update: &[],
            install: &["add", "--no-cache"],
            runtime: &["iptables"],
            build: &["build-base", "libcap-dev", "libnetfilter_queue-dev", "libnfnetlink-dev", "zlib-dev", "libmnl-dev", "iptables"],
        }),
        "pacman" => Some(Pm {
            cmd: "pacman",
            update: &[],
            install: &["-Sy", "--needed", "--noconfirm"],
            runtime: &["iptables"],
            build: &["base-devel", "libcap", "libnetfilter_queue", "libnfnetlink", "zlib", "libmnl", "iptables"],
        }),
        "zypper" => Some(Pm {
            cmd: "zypper",
            update: &[],
            install: &["--non-interactive", "install"],
            runtime: &["iptables"],
            build: &[
                "gcc",
                "make",
                "libcap-devel",
                "libnetfilter_queue-devel",
                "libnfnetlink-devel",
                "zlib-devel",
                "libmnl-devel",
                "iptables",
            ],
        }),
        _ => None,
    }
}

fn rpm_pm(cmd: &'static str) -> Pm {
    Pm {
        cmd,
        update: &[],
        install: &["install", "-y"],
        runtime: &["iptables"],
        build: &["gcc", "make", "libcap-devel", "libnetfilter_queue-devel", "libnfnetlink-devel", "zlib-devel", "libmnl-devel", "iptables"],
    }
}

fn command_exists(cmd: &str) -> bool {
    match Command::new(cmd).arg("--version").stdin(Stdio::null()).stdout(Stdio::null()).stderr(Stdio::null()).status() {
        Ok(_) => true,
        Err(e) => e.kind() != std::io::ErrorKind::NotFound,
    }
}

fn package_manager() -> Option<Pm> {
    ["apt-get", "dnf", "yum", "apk", "pacman", "zypper"].into_iter().find_map(|cmd| {
        let pm = pm_spec(cmd)?;
        command_exists(cmd).then_some(pm)
    })
}

/// What to tell the admin when the host is not a distro the installer can install on.
fn no_pm_message(why: &str) -> String {
    format!(
        "DPI: {why}. This system has no supported package manager (apt-get, dnf, yum, apk, pacman, zypper). \
On Debian or Ubuntu install iptables; compiling nfqws also needs build-essential, libcap-dev, \
libnetfilter-queue-dev, libnfnetlink-dev, zlib1g-dev and libmnl-dev"
    )
}

fn pm_install(pm: &Pm, pkgs: &[&str]) -> Result<()> {
    if !pm.update.is_empty() {
        run_env(pm.cmd, pm.update, &[("DEBIAN_FRONTEND", "noninteractive")])?;
    }
    let mut args = Vec::with_capacity(pm.install.len() + pkgs.len());
    args.extend_from_slice(pm.install);
    args.extend_from_slice(pkgs);
    run_env(pm.cmd, &args, &[("DEBIAN_FRONTEND", "noninteractive")])
}

/// `uname -m` as the directory zapret ships a static nfqws under. None means the release
/// has no binary for it and the installer compiles.
fn prebuilt_arch(machine: &str) -> Option<&'static str> {
    match machine {
        "x86_64" | "amd64" => Some("linux-x86_64"),
        "aarch64" | "arm64" => Some("linux-arm64"),
        "armv7l" | "armv6l" => Some("linux-arm"),
        "i386" | "i686" | "i586" => Some("linux-x86"),
        _ => None,
    }
}

fn machine() -> Result<String> {
    let out = Command::new("uname").arg("-m").stdin(Stdio::null()).output().context("uname -m")?;
    if !out.status.success() {
        bail!("uname -m failed");
    }
    let name = String::from_utf8_lossy(&out.stdout).trim().to_string();
    if name.is_empty() {
        bail!("uname -m said nothing");
    }
    Ok(name)
}

fn set_exec(path: &Path) -> Result<()> {
    let mut perms = fs::metadata(path)?.permissions();
    perms.set_mode(perms.mode() | 0o755);
    fs::set_permissions(path, perms).with_context(|| format!("chmod {}", path.display()))
}

/// Compiles nfqws from the extracted sources. The headers are installed first; a distro
/// this installer does not know gets the package names instead of a bare compiler error.
fn compile_nfqws(src_root: &Path, dest: &Path) -> Result<()> {
    out("DPI: no prebuilt nfqws for this architecture; compiling");
    let Some(pm) = package_manager() else {
        bail!("{}", no_pm_message("nfqws has to be compiled here"));
    };
    pm_install(&pm, pm.build).context("install the headers nfqws needs (libcap, netfilter queue, libmnl, zlib)")?;
    let nfq = src_root.join("nfq");
    run("make", &["-C", nfq.to_str().unwrap_or("")]).context("build nfqws")?;
    fs::copy(nfq.join("nfqws"), dest).context("copy nfqws")?;
    set_exec(dest)
}

/// Downloads the pinned zapret sources, installs nfqws and writes the scripts. Idempotent:
/// an install that already has the binary is left alone. The binary is the static one the
/// release ships for this machine; compiling is the fallback for an architecture it does not.
fn install() -> Result<()> {
    let dir = dir();
    if dir.join("nfqws").is_file() {
        // A previous install that stopped after the binary but before the scripts would
        // leave the service without its start file; fill what is missing, never overwrite
        // a host list or a strategy the admin edited.
        write_scripts()?;
        return Ok(());
    }
    fs::create_dir_all(&dir).with_context(|| format!("create {}", dir.display()))?;
    out("DPI: installing zapret");
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
        // sha2 0.11's output is a hybrid array without LowerHex: the bytes are written out.
        h.finalize().iter().map(|b| format!("{b:02x}")).collect::<String>()
    };
    if got != ZAPRET_SHA256 {
        let _ = fs::remove_file(&tarball);
        bail!("zapret: the archive is not the pinned one (sha256 {got})");
    }
    let src = dir.join("src");
    let _ = fs::remove_dir_all(&src);
    fs::create_dir_all(&src)?;
    run("tar", &["-xzf", tarball.to_str().unwrap_or(""), "-C", src.to_str().unwrap_or("")])?;
    let src_root = src.join(format!("zapret-v{ZAPRET_VERSION}"));
    let dest = dir.join("nfqws");
    let arch = machine()?;
    match prebuilt_arch(&arch).map(|rel| src_root.join("binaries").join(rel).join("nfqws")) {
        Some(bin) if bin.is_file() => {
            fs::copy(&bin, &dest).context("copy the prebuilt nfqws")?;
            set_exec(&dest)?;
            out(&format!("DPI: using the prebuilt nfqws ({arch})"));
        }
        _ => compile_nfqws(&src_root, &dest)?,
    }
    if !command_exists("iptables") {
        let Some(pm) = package_manager() else {
            bail!("{}", no_pm_message("iptables is not installed"));
        };
        pm_install(&pm, pm.runtime).context("install iptables")?;
        if !command_exists("iptables") {
            bail!("DPI: iptables is still not installed");
        }
    }
    let _ = fs::remove_file(&tarball);
    write_scripts()?;
    out("DPI: zapret is installed");
    Ok(())
}

/// The host list, the start script and the stop script. `run` is read again on every start,
/// so a strategy edited there survives a restart.
fn write_scripts() -> Result<()> {
    let dir = dir();
    // Each file only when it is missing: an install that stopped half-way is completed, and
    // a strategy or a host list the admin edited stays.
    if !dir.join("hosts.txt").is_file() {
        let mut hosts = String::new();
        for h in HOSTS {
            hosts.push_str(h);
            hosts.push('\n');
        }
        fs::write(dir.join("hosts.txt"), hosts)?;
    }
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
        if p.is_file() {
            continue;
        }
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
         TimeoutStopSec=20\n\
         AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW\n\
         CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW\n\
         NoNewPrivileges=true\n\
         ProtectSystem=full\n\
         ProtectHome=true\n\
         PrivateTmp=true\n\
         ProtectKernelTunables=true\n\
         ProtectKernelModules=true\n\
         ProtectControlGroups=true\n\
         RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK\n\
         RestrictNamespaces=true\n\
         LockPersonality=true\n\
         RestrictRealtime=true\n\
         SystemCallArchitectures=native\n\
         ReadOnlyPaths={dir}\n\
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

/// Stops the service and removes its unit: an uninstall must leave no privileged process
/// and no rule behind, with or without --purge.
pub fn remove() -> Result<()> {
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
    let _ = fs::remove_file("/etc/systemd/system/arikan-dpi.service");
    let _ = run("systemctl", &["daemon-reload"]);
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

    // The release ships static binaries for the machines a node usually is. Anything else
    // is compiled, and every distro we install on is asked for libcap: that header is what
    // a bare server's build dies on.
    #[test]
    fn prebuilt_arches_match_the_release() {
        assert_eq!(prebuilt_arch("x86_64"), Some("linux-x86_64"));
        assert_eq!(prebuilt_arch("amd64"), Some("linux-x86_64"));
        assert_eq!(prebuilt_arch("aarch64"), Some("linux-arm64"));
        assert_eq!(prebuilt_arch("arm64"), Some("linux-arm64"));
        assert_eq!(prebuilt_arch("armv7l"), Some("linux-arm"));
        assert_eq!(prebuilt_arch("armv6l"), Some("linux-arm"));
        assert_eq!(prebuilt_arch("i686"), Some("linux-x86"));
        assert_eq!(prebuilt_arch("riscv64"), None);
        assert_eq!(prebuilt_arch(""), None);
    }

    #[test]
    fn every_build_installs_libcap() {
        for cmd in ["apt-get", "dnf", "yum", "apk", "pacman", "zypper"] {
            let pm = pm_spec(cmd).unwrap_or_else(|| panic!("no spec for {cmd}"));
            let build = pm.build.join(" ");
            assert!(build.contains("cap"), "{cmd} does not install libcap: {build}");
            assert!(pm.runtime.contains(&"iptables"), "{cmd}");
        }
        let apt = pm_spec("apt-get").unwrap();
        for pkg in ["build-essential", "libcap-dev", "libnetfilter-queue-dev", "libnfnetlink-dev", "zlib1g-dev", "libmnl-dev", "iptables"] {
            assert!(apt.build.contains(&pkg), "apt build deps miss {pkg}");
        }
        assert!(pm_spec("not-a-manager").is_none());
    }

    #[test]
    fn an_unknown_distro_is_told_what_to_install() {
        let msg = no_pm_message("iptables is not installed");
        assert!(msg.contains("iptables is not installed"));
        for word in ["apt-get", "dnf", "apk", "pacman", "libcap-dev", "libnetfilter-queue-dev", "zlib1g-dev", "libmnl-dev"] {
            assert!(msg.contains(word), "missing {word} in {msg}");
        }
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
