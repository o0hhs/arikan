//! `arikan doctor`: checks the server of a panel or a node and says what is wrong and what
//! to do about it. The checks that wait for the network (DNS, GitHub, GHCR) run beside the
//! local ones, so that the whole run stays short.

use std::io::Read;
use std::net::{IpAddr, Ipv4Addr, ToSocketAddrs};
use std::path::Path;
use std::process::{Command, Stdio};
use std::sync::mpsc;
use std::thread;
use std::time::{Duration, Instant, SystemTime};

use anyhow::{Error, Result, bail};
use serde::{Deserialize, Serialize};

use crate::docker::{self, Service};
use crate::ops::Install;
use crate::panelsettings::Address;
use crate::setup::mark;
use crate::system::{self, Level, Proto};
use crate::{DIR, acme, addon, clock, hello, host, hostreq, net, redact};

/// How long one request to the outside may take.
const NET_TIMEOUT: Duration = Duration::from_secs(8);

/// All the network answers are in by then, or they count as missing.
const NET_DEADLINE: Duration = Duration::from_secs(12);

/// A server clock this far from GitHub's is wrong: TLS and REALITY fail.
const SKEW_LIMIT: i64 = 30;

const MIB: u64 = 1024 * 1024;

/// One line of the report.
#[derive(Debug, Clone, PartialEq)]
struct Item {
    level: Level,
    label: String,
    detail: String,
    /// What to do; printed for anything that is not fine.
    fix: Option<String>,
}

impl Item {
    fn ok(label: impl Into<String>, detail: impl Into<String>) -> Self {
        Self { level: Level::Ok, label: label.into(), detail: detail.into(), fix: None }
    }

    fn warn(label: impl Into<String>, detail: impl Into<String>, fix: impl Into<String>) -> Self {
        Self { level: Level::Warn, label: label.into(), detail: detail.into(), fix: Some(fix.into()) }
    }

    fn error(label: impl Into<String>, detail: impl Into<String>, fix: impl Into<String>) -> Self {
        Self { level: Level::Error, label: label.into(), detail: detail.into(), fix: Some(fix.into()) }
    }

    /// The text as printed: the line, and under a problem the fix.
    fn lines(&self) -> Vec<String> {
        let mut out = vec![format!("{} {:<16}  {}", mark(self.level), self.label, self.detail).trim_end().to_owned()];
        if let (Some(fix), true) = (&self.fix, self.level != Level::Ok) {
            out.push(format!("    fix: {fix}"));
        }
        out
    }
}

/// Keeps the items, prints them as they come when someone is reading, and counts the
/// problems.
#[derive(Default)]
struct Report {
    errors: usize,
    warnings: usize,
    items: Vec<Item>,
    /// Print each item at once (`mikan doctor`); not for the JSON the panel reads.
    live: bool,
}

impl Report {
    fn add(&mut self, items: impl IntoIterator<Item = Item>) {
        for item in items {
            match item.level {
                Level::Error => self.errors += 1,
                Level::Warn => self.warnings += 1,
                Level::Ok => {}
            }
            if self.live {
                for line in item.lines() {
                    crate::out(&line);
                }
            }
            self.items.push(item);
        }
    }
}

/// One line of the JSON report, as the panel reads it (nodeapi.HostItem).
#[derive(Serialize, Debug, PartialEq)]
pub struct JsonItem {
    pub level: &'static str,
    pub label: String,
    pub detail: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub fix: String,
}

/// What `mikan doctor --json` prints and `--requested` writes to doctor/report.json
/// (nodeapi.HostReport): the same checks as the text, with the secrets cut out.
#[derive(Serialize, Debug, PartialEq)]
pub struct JsonReport {
    /// The request this answers; "" for a run nobody asked for.
    pub id: String,
    pub at: String,
    pub version: String,
    pub kind: &'static str,
    pub errors: usize,
    pub warnings: usize,
    pub items: Vec<JsonItem>,
}

fn level_name(l: Level) -> &'static str {
    match l {
        Level::Ok => "ok",
        Level::Warn => "warn",
        Level::Error => "error",
    }
}

impl Report {
    /// The report as data. Everything that is text goes through redact: the report leaves
    /// the server, and what a container said (a node's contact lines) is in it.
    fn json(&self, install: &Install, id: &str) -> JsonReport {
        JsonReport {
            id: id.to_owned(),
            at: clock::rfc3339(),
            version: install.version(),
            kind: if install.node { "node" } else { "panel" },
            errors: self.errors,
            warnings: self.warnings,
            items: self
                .items
                .iter()
                .map(|i| JsonItem {
                    level: level_name(i.level),
                    label: redact::text(&i.label),
                    detail: redact::text(&i.detail),
                    // A fine line has no fix under it, in the JSON as in the text.
                    fix: if i.level == Level::Ok { String::new() } else { i.fix.as_deref().map(redact::text).unwrap_or_default() },
                })
                .collect(),
        }
    }
}

fn background<T: Send + 'static>(f: impl FnOnce() -> T + Send + 'static) -> mpsc::Receiver<T> {
    let (tx, rx) = mpsc::channel();
    thread::spawn(move || {
        let _ = tx.send(f());
    });
    rx
}

/// The answer of a background check; None when it is not in by the deadline.
fn answer<T>(rx: &mpsc::Receiver<T>, deadline: Instant) -> Option<T> {
    rx.recv_timeout(deadline.saturating_duration_since(Instant::now())).ok()
}

type Reached = Result<net::Probe, String>;

/// `mikan doctor`: the checks, printed as they finish.
pub fn run() -> Result<()> {
    let install = Install::load()?;
    let mut report = Report { live: true, ..Report::default() };
    crate::out(&format!("mikan {}, {}", install.version(), if install.node { "node" } else { "panel" }));
    gather(&install, &mut report);
    match (report.errors, report.warnings) {
        (0, 0) => crate::out("No problems found."),
        (0, w) => crate::out(&format!("No errors; {w} thing{} to look at above.", if w == 1 { "" } else { "s" })),
        (e, _) => bail!("{e} problem{} found", if e == 1 { "" } else { "s" }),
    }
    Ok(())
}

/// The checks as data for id's asker (the panel; "" for a run from the shell).
pub fn collect(install: &Install, id: &str) -> JsonReport {
    let mut report = Report::default();
    gather(install, &mut report);
    report.json(install, id)
}

/// `mikan doctor --json`: the report as JSON on stdout, exit status as for the text.
pub fn json() -> Result<()> {
    let r = collect(&Install::load()?, "");
    crate::out(&serde_json::to_string_pretty(&r)?);
    if r.errors > 0 {
        bail!("{} problem{} found", r.errors, if r.errors == 1 { "" } else { "s" });
    }
    Ok(())
}

/// `mikan doctor --requested` (the doctor request unit): runs the checks the panel, or a
/// node, asked for and writes doctor/report.json for it. A request that is not one is
/// removed and nothing runs.
pub fn requested() -> Result<()> {
    let install = Install::load()?;
    let Some(dir) = hostreq::dir(install.node, "doctor")? else { return Ok(()) };
    let Some(req) = hostreq::take(&dir)? else { return Ok(()) };
    let report = collect(&install, &req.id);
    dir.write("report.json", serde_json::to_string(&report)?.as_bytes(), 0o644)?;
    crate::out(&format!("doctor report written: {} problem(s), {} warning(s)", report.errors, report.warnings));
    Ok(())
}

/// All the checks of this server into report.
fn gather(install: &Install, report: &mut Report) {
    let deadline = Instant::now() + NET_DEADLINE;
    let github = background(|| net::probe("https://github.com", NET_TIMEOUT).map_err(|e| e.to_string()));
    let ghcr = background(|| net::probe("https://ghcr.io/v2/", NET_TIMEOUT).map_err(|e| e.to_string()));
    let dns = background(|| "github.com:443".to_socket_addrs().map(|a| a.count()).map_err(|e| e.to_string()));

    let engine = docker::version();
    report.add([docker_item(engine.as_deref())]);
    if engine.is_some() {
        report.add(containers(docker::services().map_err(|e| format!("{e:#}"))));
    }
    report.add(ports(install, engine.is_some()));
    if install.node
        && let Some(item) = site_item()
    {
        report.add([item]);
    }
    report.add([port80_item(system::port_owner(80, Proto::Tcp).as_deref(), install.config.get("MIKAN_ACME_LISTEN"))]);
    report.add(firewall(install));

    // The panel's own address from its database (a container start), and beside the other
    // network checks where public DNS sends its domain. Without the image there is nothing
    // to run the read in, and `compose run` would pull it: the missing panel is said above.
    let address = (!install.node && engine.is_some() && install.config.get("MIKAN_IMAGE").is_some_and(docker::image_present))
        .then(|| crate::panelsettings::read(Duration::from_secs(30)));
    let address_net = address.as_ref().and_then(|a| a.as_ref().ok()).map(|a| {
        let a = a.clone();
        background(move || {
            let server = net::public_ipv4()?;
            let name = a.host().to_owned();
            let dns = name.parse::<IpAddr>().is_err().then(|| net::check_domain(&name, server));
            Some((server, dns))
        })
    });

    let github = answer(&github, deadline);
    let skew = github
        .as_ref()
        .and_then(|r| r.as_ref().ok())
        .and_then(|p| p.date.as_deref())
        .and_then(|d| skew_secs(d, clock::unix(std::time::SystemTime::now())));
    report.add([clock_item(system::clock_synced(), skew)]);
    report.add([dns_item(answer(&dns, deadline))]);
    report.add([
        outbound_item("GitHub", "github.com", "updates cannot be downloaded", github),
        outbound_item("GHCR", "ghcr.io", "images cannot be pulled", answer(&ghcr, deadline)),
    ]);

    report.add([disk_item(system::disk_free_bytes(Path::new(DIR))), memory_item(system::mem_available_mb())]);
    if install.node {
        report.add(contact(install.node_port(), engine.is_some()));
        let lock = crate::apilock::report(install);
        report.add([Item { level: lock.level, label: "API lockdown".into(), detail: lock.detail, fix: lock.fix }]);
    } else if let Some(address) = &address {
        // The DNS for the domain gets its time from now: the local checks above may have
        // taken most of the shared deadline the network checks had.
        let until = Instant::now() + NET_DEADLINE;
        report.add(address_items(address, install, address_net.and_then(|rx| answer(&rx, until)).flatten()));
    }
}

/// What the node wrote about its own page into data/node/site.json (internal/node/site.go).
#[derive(Deserialize, Default, Debug)]
struct SiteState {
    #[serde(default)]
    listen: String,
    #[serde(default)]
    port: u16,
    #[serde(default)]
    ok: bool,
    #[serde(default)]
    error: String,
}

/// The node's own page (data/node/www over https): the node writes where it answers — or why
/// it does not — into data/node/site.json, as the first contact with the panel goes into
/// hello.json. A node that has not started one says nothing.
fn site_item() -> Option<Item> {
    let dir = crate::panelfs::Dir::open(Path::new(DIR), "data/node", false).ok()??;
    let raw = dir.read("site.json", 4 << 10).ok()??;
    let site: SiteState = serde_json::from_slice(&raw).ok()?;
    Some(site_lines(&site))
}

/// What the admin reads about the page.
fn site_lines(site: &SiteState) -> Item {
    if site.ok {
        let dest = if site.port > 0 { format!(", a REALITY dest may name 127.0.0.1:{}", site.port) } else { String::new() };
        Item::ok("Page", format!("https://{}{}", site.listen, dest))
    } else {
        Item::warn(
            "Page",
            format!("the node's own page does not run: {}", site.error),
            "free the port, or set MIKAN_SITE_LISTEN in .env (off serves none)",
        )
    }
}

fn docker_item(version: Option<&str>) -> Item {
    match version {
        Some(v) => Item::ok("Docker", format!("engine {v}")),
        None => Item::error("Docker", "the daemon does not answer", "systemctl start docker; journalctl -u docker -n 50"),
    }
}

fn containers(services: Result<Vec<Service>, String>) -> Vec<Item> {
    match services {
        Err(e) => vec![Item::error("Containers", e, "arikan restart")],
        Ok(list) if list.is_empty() => vec![Item::error("Containers", "none exist", "arikan restart")],
        Ok(list) => list.iter().map(container).collect(),
    }
}

fn container(s: &Service) -> Item {
    let label = format!("Container {}", s.name);
    let fix = format!("arikan restart; arikan logs {}", s.name);
    let status = if s.status.is_empty() { s.state.clone() } else { s.status.clone() };
    if s.state != "running" || s.status.contains("unhealthy") {
        Item::error(label, status, fix)
    } else if s.status.contains("starting") {
        Item::warn(label, status, "wait a minute and run arikan doctor again; arikan logs ".to_owned() + &s.name)
    } else {
        Item::ok(label, status)
    }
}

/// The port the server answers on: a node's API port, the panel's own.
fn ports(install: &Install, engine: bool) -> Vec<Item> {
    if install.node {
        return vec![match install.node_port() {
            Some(p) => match system::port_owner(p, Proto::Tcp) {
                Some(who) => Item::ok("API port", format!("{p}/tcp is listening ({who})")),
                None => Item::error(
                    "API port",
                    format!("nothing listens on {p}/tcp, where the panel connects"),
                    "arikan logs node; arikan restart",
                ),
            },
            None => Item::error(
                "API port",
                "NODE_API_PORT is not set in /opt/mikan/config.json",
                "arikan join <new key from the panel's Nodes page>",
            ),
        }];
    }
    let port: Option<u16> = install.config.get("PANEL_PORT").and_then(|p| p.parse().ok());
    if !engine {
        return Vec::new();
    }
    let at = port.map_or_else(String::new, |p| format!(" on port {p}"));
    vec![if docker::panel_healthy() {
        Item::ok("Panel", format!("answers{at}"))
    } else {
        let listening = port.and_then(|p| system::port_owner(p, Proto::Tcp));
        let detail = match (port, listening) {
            (Some(p), None) => format!("does not answer, nothing listens on {p}/tcp"),
            (Some(p), Some(who)) => format!("does not answer on {p}/tcp ({who} listens there)"),
            (None, _) => "does not answer".to_owned(),
        };
        Item::error("Panel", detail, "arikan logs panel; arikan restart")
    }]
}

/// The panel's own address as its database keeps it, against what this server serves: the
/// port the panel listens on (PANEL_PORT in config.json) and the address every link, the
/// admin path and each node's address are built from. A restore from another server and
/// `arikan admin migrate --port` are what leave the two apart; no other check reads the
/// panel's settings.
fn address_items(address: &Result<Address, Error>, install: &Install, seen: Option<(Ipv4Addr, Option<net::DomainCheck>)>) -> Vec<Item> {
    let address = match address {
        Ok(a) => a,
        Err(e) => {
            return vec![Item::warn(
                "Panel address",
                format!("the address the database keeps cannot be read: {e:#}"),
                "arikan update: the panel's image reads its own address",
            )];
        }
    };
    let mut items = Vec::new();
    let own = install.config.get("PANEL_PORT").and_then(|p| p.parse::<u16>().ok()).filter(|p| *p > 0);
    match (own, (address.panel_port > 0).then_some(address.panel_port)) {
        (Some(own), Some(db)) if own == db => items.push(Item::ok("Panel port", format!("{own}, as the database says"))),
        (Some(own), Some(db)) => items.push(Item::warn(
            "Panel port",
            format!(
                "the panel listens on {own}, while every link, the admin path and each node's address are built with {db} from the database"
            ),
            format!("arikan restart: the setting follows the listener; if the links must keep {db}, put PANEL_PORT={db} in /opt/mikan/config.json first"),
        )),
        (Some(own), None) => items.push(Item::warn(
            "Panel port",
            format!("the database names no port, and the panel listens on {own}: its links are built without a port"),
            format!(
                "a new panel: arikan admin bootstrap; a restored one: arikan admin migrate --public-host <this server's address> --port {own}"
            ),
        )),
        (None, Some(db)) => items.push(Item::error(
            "Panel port",
            format!("PANEL_PORT is not set in /opt/mikan/config.json, and the database names {db}"),
            format!("put PANEL_PORT={db} in /opt/mikan/config.json and run arikan restart"),
        )),
        (None, None) => {}
    }
    // Where the links send clients: the domain, which public DNS is asked about, or the
    // public address, which must be this server's.
    let host = address.host();
    if host.is_empty() {
        return items;
    }
    let server = seen.as_ref().map(|(ip, _)| *ip);
    match (host.parse::<IpAddr>(), server) {
        (Ok(ip), Some(server)) if ip == IpAddr::V4(server) => items.push(Item::ok("Panel address", format!("{host} is this server"))),
        (Ok(IpAddr::V6(ip)), _) => {
            if system::global_ipv6().contains(&ip) {
                items.push(Item::ok("Panel address", format!("{host} is this server")));
            } else {
                items.push(Item::error(
                    "Panel address",
                    format!("every link is built from {host}, which is not an address of this server"),
                    "arikan admin migrate --public-host <this server's address>",
                ));
            }
        }
        (Ok(_), Some(server)) => items.push(Item::error(
            "Panel address",
            format!("every link is built from {host}, not from this server ({server})"),
            format!("arikan admin migrate --public-host {server}"),
        )),
        (Ok(_), None) => {}
        (Err(_), _) => match seen.as_ref().and_then(|(_, dns)| dns.as_ref()) {
            Some(dns) => items.extend(dns.notes.iter().map(|note| match note.level {
                Level::Ok => Item::ok("Panel domain", note.text.clone()),
                _ => Item { level: note.level, label: "Panel domain".into(), detail: note.text.clone(), fix: Some(dns_fix(host, server)) },
            })),
            None => items.push(Item::warn(
                "Panel domain",
                format!("{host} is the address every link is built from, and this server's own address cannot be asked to check it"),
                "check the outbound network: curl -s https://api.ipify.org; then arikan doctor",
            )),
        },
    }
    items
}

/// What to do about a domain that does not lead to this server: the record is changed at
/// the DNS host, not here.
fn dns_fix(domain: &str, server: Option<Ipv4Addr>) -> String {
    match server {
        Some(ip) => format!("make the A record of {domain} point to {ip} at the DNS host, then arikan doctor"),
        None => format!("make the A record of {domain} point to this server at the DNS host"),
    }
}

/// Port 80, where Let's Encrypt checks the server before it gives a certificate: free for
/// Arikan, or a web server that passes the check on.
fn port80_item(owner: Option<&str>, listen: Option<&str>) -> Item {
    const LABEL: &str = "Port 80";
    const FIX: &str = "arikan cert proxy <the server's domain, as the panel shows it>";
    let behind = listen.map(str::trim).filter(|l| !l.is_empty() && *l != ":80").unwrap_or_default();
    match acme::port80(owner, listen, || 0) {
        acme::Port80::Own { drop: false } => Item::ok(LABEL, "free: Arikan answers Let's Encrypt there when it gets a certificate"),
        acme::Port80::Own { drop: true } => Item::warn(
            LABEL,
            format!("free, yet Arikan waits for Let's Encrypt on {behind} behind a web server that is gone: no public certificate"),
            FIX,
        ),
        acme::Port80::Front(front, _) if !behind.is_empty() => {
            Item::ok(LABEL, format!("{} holds it and passes Let's Encrypt to Arikan on {behind}", front.name()))
        }
        acme::Port80::Front(front, _) => {
            Item::warn(LABEL, format!("{} holds it, and Let's Encrypt checks the server there: no public certificate", front.name()), FIX)
        }
        acme::Port80::Other(who) => Item::warn(
            LABEL,
            format!("held by {who}, and Let's Encrypt checks the server there: no public certificate"),
            "free port 80, or pass /.well-known/acme-challenge/ to Arikan (MIKAN_ACME_LISTEN)",
        ),
    }
}

/// A port or a range the firewall must let through.
#[derive(Debug, Clone, PartialEq)]
struct Want {
    what: String,
    lo: u16,
    hi: u16,
    /// None: the protocol is not known, any rule for the port will do.
    proto: Option<Proto>,
    /// The rule must be for everyone; a rule for the panel's address alone is enough when not.
    anyone: bool,
    /// How bad it is when missing.
    level: Level,
}

impl Want {
    /// The rule as ufw writes it: 443, 443/tcp, 20000:20100/udp.
    fn rule(&self) -> String {
        let ports = if self.lo == self.hi { self.lo.to_string() } else { format!("{}:{}", self.lo, self.hi) };
        match self.proto {
            Some(p) => format!("{ports}/{}", p.name()),
            None => ports,
        }
    }

    /// The commands that open it; ufw wants a protocol for a range.
    fn commands(&self) -> Vec<String> {
        if self.proto.is_none() && self.lo != self.hi {
            let ports = format!("{}:{}", self.lo, self.hi);
            return vec![format!("ufw allow {ports}/tcp"), format!("ufw allow {ports}/udp")];
        }
        vec![format!("ufw allow {}", self.rule())]
    }
}

/// An allow rule of `ufw status`.
#[derive(Debug, Clone, PartialEq)]
struct Rule {
    lo: u16,
    hi: u16,
    /// None: both.
    proto: Option<Proto>,
    /// From anywhere, not from one address.
    anywhere: bool,
}

/// The allow rules in the text of `ufw status`. Deny rules, rules for outgoing traffic and
/// application profiles are left out.
fn parse_ufw(text: &str) -> Vec<Rule> {
    let mut rules = Vec::new();
    for line in text.lines() {
        let words: Vec<&str> = line.split_whitespace().collect();
        let Some(at) = words.iter().position(|w| matches!(*w, "ALLOW" | "LIMIT" | "DENY" | "REJECT")) else { continue };
        if at == 0 || matches!(words[at], "DENY" | "REJECT") {
            continue;
        }
        let mut from = &words[at + 1..];
        match from.first() {
            Some(&"OUT") => continue,
            Some(&("IN" | "FWD")) => from = &from[1..],
            _ => {}
        }
        let anywhere = from.first().is_some_and(|f| f.eq_ignore_ascii_case("anywhere"));
        for (lo, hi, proto) in parse_to(words[0]) {
            rules.push(Rule { lo, hi, proto, anywhere });
        }
    }
    rules
}

/// The "To" column: 443, 443/tcp, 20000:20100/udp, 80,443/tcp, or Anywhere for every port.
fn parse_to(spec: &str) -> Vec<(u16, u16, Option<Proto>)> {
    if spec.eq_ignore_ascii_case("anywhere") {
        return vec![(1, u16::MAX, None)];
    }
    let (ports, proto) = match spec.split_once('/') {
        Some((p, "tcp")) => (p, Some(Proto::Tcp)),
        Some((p, "udp")) => (p, Some(Proto::Udp)),
        Some(_) => return Vec::new(),
        None => (spec, None),
    };
    ports
        .split(',')
        .filter_map(|part| match part.split_once(':') {
            Some((a, b)) => Some((a.parse().ok()?, b.parse().ok()?, proto)),
            None => part.parse().ok().map(|p| (p, p, proto)),
        })
        .filter(|(lo, hi, _)| lo <= hi && *lo > 0)
        .collect()
}

/// Whether every port of the wish is let through by some rule.
fn allowed(rules: &[Rule], w: &Want) -> bool {
    (w.lo..=w.hi).all(|port| {
        rules.iter().any(|r| {
            r.lo <= port
                && port <= r.hi
                && (r.anywhere || !w.anyone)
                && match (w.proto, r.proto) {
                    (Some(a), Some(b)) => a == b,
                    _ => true,
                }
        })
    })
}

/// The part of node-state.json the firewall check reads: the ports of the inbounds and of
/// the relay.
#[derive(Deserialize, Default, Debug)]
struct NodeState {
    #[serde(default)]
    inbounds: Vec<StateInbound>,
    relay: Option<StateRelay>,
}

#[derive(Deserialize, Default, Debug)]
struct StateInbound {
    #[serde(default)]
    name: String,
    listen: Option<String>,
    #[serde(default)]
    port: String,
    config: Option<serde_json::Value>,
    preset: Option<String>,
}

#[derive(Deserialize, Default, Debug)]
struct StateRelay {
    #[serde(default)]
    port: String,
}

impl StateInbound {
    /// UDP for the QUIC protocols, TCP for the ones known to run over it; None when the file
    /// does not say (then any rule for the port will do).
    fn proto(&self) -> Option<Proto> {
        let kind = self.config.as_ref().and_then(|c| c.get("type")).and_then(|t| t.as_str()).or(self.preset.as_deref());
        match kind? {
            "hysteria2" | "tuic" | "tuic_v5" | "shadowquic" => Some(Proto::Udp),
            "vless" | "vmess" | "trojan" | "anytls" | "trusttunnel" | "vless_reality_vision" | "vless_reality_xhttp" => Some(Proto::Tcp),
            _ => None,
        }
    }

    fn local(&self) -> bool {
        self.listen.as_deref().is_some_and(|l| l.starts_with("127.") || l == "::1" || l == "localhost")
    }
}

/// "443" or "20000-20100" (or with a colon).
fn parse_ports(s: &str) -> Option<(u16, u16)> {
    let (lo, hi) = match s.trim().split_once(['-', ':']) {
        Some((a, b)) => (a.trim().parse().ok()?, b.trim().parse().ok()?),
        None => {
            let p = s.trim().parse().ok()?;
            (p, p)
        }
    };
    (lo > 0 && lo <= hi).then_some((lo, hi))
}

/// What the firewall must let through: the port the panel or its panel connects to, and the
/// ports of the inbounds the node listens on.
fn wants(api: Option<u16>, panel: Option<u16>, state: Option<&NodeState>) -> Vec<Want> {
    let mut out: Vec<Want> = Vec::new();
    let mut add = |what: String, (lo, hi): (u16, u16), proto: Option<Proto>, anyone: bool, level: Level| {
        let w = Want { what, lo, hi, proto, anyone, level };
        if !out.iter().any(|o| (o.lo, o.hi, o.proto) == (w.lo, w.hi, w.proto)) {
            out.push(w);
        }
    };
    if let Some(p) = api {
        // The panel's address alone may be let in.
        add("the node's API port".into(), (p, p), Some(Proto::Tcp), false, Level::Error);
    }
    if let Some(p) = panel {
        add("the panel's port".into(), (p, p), Some(Proto::Tcp), true, Level::Error);
    }
    if let Some(state) = state {
        for i in state.inbounds.iter().filter(|i| !i.local()) {
            if let Some(range) = parse_ports(&i.port) {
                add(format!("inbound {}", i.name), range, i.proto(), true, Level::Warn);
            }
        }
        if let Some(range) = state.relay.as_ref().and_then(|r| parse_ports(&r.port)) {
            add("the cascade relay".into(), range, None, true, Level::Warn);
        }
    }
    out
}

/// The verdicts for the wishes against the text of `ufw status`: all open, or what is not.
fn firewall_items(status: &str, wanted: &[Want]) -> Vec<Item> {
    let rules = parse_ufw(status);
    let missing: Vec<&Want> = wanted.iter().filter(|w| !allowed(&rules, w)).collect();
    if missing.is_empty() {
        let list: Vec<String> = wanted.iter().map(Want::rule).collect();
        return vec![Item::ok("Firewall (ufw)", if list.is_empty() { "active".to_owned() } else { format!("open: {}", list.join(", ")) })];
    }
    [Level::Error, Level::Warn]
        .into_iter()
        .filter_map(|level| {
            let group: Vec<&&Want> = missing.iter().filter(|w| w.level == level).collect();
            if group.is_empty() {
                return None;
            }
            let list: Vec<String> = group.iter().map(|w| format!("{} ({})", w.rule(), w.what)).collect();
            let commands: Vec<String> = group.iter().flat_map(|w| w.commands()).collect();
            let detail = format!("no rule lets through {}", list.join(", "));
            Some(if level == Level::Error {
                Item::error("Firewall (ufw)", detail, commands.join("; "))
            } else {
                Item::warn("Firewall (ufw)", detail, commands.join("; "))
            })
        })
        .collect()
}

fn firewall(install: &Install) -> Vec<Item> {
    if !install.ufw() {
        return Vec::new();
    }
    let Some(status) = system::ufw_status() else {
        return vec![Item::ok("Firewall (ufw)", "off; a firewall in the hoster's panel is not checked here")];
    };
    // The node's container writes the file: read as panelfs reads it, no link, FIFO or huge file.
    let state: Option<NodeState> = crate::panelfs::Dir::open(Path::new(DIR), "data/node", false)
        .ok()
        .flatten()
        .and_then(|d| d.read("node-state.json", 16 << 20).ok().flatten())
        .and_then(|raw| serde_json::from_slice(&raw).ok());
    let panel = (!install.node).then(|| install.config.get("PANEL_PORT").and_then(|p| p.parse().ok())).flatten();
    let mut items = firewall_items(&status, &wants(install.node_port(), panel, state.as_ref()));
    if let Some(port) = install.node_port() {
        let seen = crate::paneladdr::current();
        let addrs = seen.as_ref().map(crate::paneladdr::Seen::addrs).unwrap_or_default();
        items.extend(api_port_items(
            &status,
            port,
            &addrs,
            seen.as_ref().and_then(crate::paneladdr::Seen::last),
            &install.panel_ips(),
            clock::unix(SystemTime::now()),
        ));
    }
    if !install.node {
        let bridge = addon::load_state().map(|s| s.panel_api_bridge).unwrap_or_default();
        items.extend(addons_item(&status, &bridge));
    }
    items
}

/// How fresh a panel connection must be to tell the panel's address by it: the panel asks the
/// node for its health every few seconds.
const SEEN_FRESH: i64 = 15 * 60;

/// What to say about a node's API port: it belongs to the panel alone, and the panel is only
/// where the node last saw it. A rule for everyone is what a scanner finds; a rule for an
/// address the panel no longer comes from is a port the panel cannot reach at all, and only
/// the console can open it again.
fn api_port_items(status: &str, port: u16, seen: &[IpAddr], last: Option<i64>, scoped: &[IpAddr], now: i64) -> Vec<Item> {
    const LABEL: &str = "Firewall (ufw)";
    let rules = host::port_rules(status, port);
    if rules.is_empty() {
        // Nothing to do with the port at all: the check for the port being open speaks.
        return Vec::new();
    }
    let fix = |ips: &[IpAddr]| crate::paneladdr::fix_commands(port, ips).join("; ");
    if rules.iter().any(Option::is_none) {
        return vec![Item::warn(LABEL, format!("the node's API port {port} is open to everyone"), fix(seen))];
    }
    let from: Vec<IpAddr> = rules.iter().flatten().copied().collect();
    let list = |ips: &[IpAddr]| ips.iter().map(ToString::to_string).collect::<Vec<_>>().join(", ");
    let fresh = last.is_some_and(|t| now - t <= SEEN_FRESH);
    if fresh && !seen.is_empty() && !from.iter().any(|ip| seen.contains(ip)) {
        return vec![Item::warn(
            LABEL,
            format!("the node's API port {port} is open for {}, but the panel connects from {}", list(&from), list(seen)),
            fix(seen),
        )];
    }
    if last.is_some_and(|t| now - t > SEEN_FRESH) && !scoped.is_empty() {
        return vec![Item::warn(
            LABEL,
            format!(
                "the panel has not reached this node since {} (the rule is for {}): if it cannot, open port {port} from the console and it settles by itself on the next update",
                clock::Utc::from_unix(last.unwrap_or(0)).stamp(),
                list(&from)
            ),
            format!("ufw allow {port}/tcp"),
        )];
    }
    Vec::new()
}

/// Custom addons call the panel at the bridge's gateway, which ufw's default `deny incoming`
/// drops unless a rule lets the bridge in; the host makes the rule with the addon. None while
/// no custom addon runs (panel_api_bridge is empty then).
fn addons_item(status: &str, panel_api_bridge: &str) -> Option<Item> {
    const LABEL: &str = "Firewall (ufw)";
    let door = addon::door(panel_api_bridge)?;
    Some(if addon::parse_doors(status).contains(&door) {
        Item::ok(LABEL, format!("custom addons reach the panel at {panel_api_bridge}"))
    } else {
        Item::warn(
            LABEL,
            format!("no rule lets the custom addons' network reach the panel at {panel_api_bridge}: they cannot call it"),
            addon::allow_command(door),
        )
    })
}

/// Seconds this server's clock is ahead of the other side's, from its Date header.
fn skew_secs(date: &str, now: i64) -> Option<i64> {
    Some(now - clock::parse_http_date(date)?)
}

fn clock_item(synced: Option<bool>, skew: Option<i64>) -> Item {
    const FIX: &str = "timedatectl set-ntp true";
    let by = |s: i64| format!("{} s {} GitHub's clock", s.abs(), if s > 0 { "ahead of" } else { "behind" });
    match (synced, skew) {
        (_, Some(s)) if s.abs() >= SKEW_LIMIT => Item::error("Clock", format!("{}: TLS and REALITY need the right time", by(s)), FIX),
        (Some(false), s) => {
            let near = s.map(|s| format!(", {}", by(s))).unwrap_or_default();
            Item::warn("Clock", format!("not synchronized with a time server{near}"), FIX)
        }
        (Some(true), Some(s)) => Item::ok("Clock", format!("synchronized, within {} s of GitHub's clock", s.abs())),
        (Some(true), None) => Item::ok("Clock", "synchronized"),
        (None, Some(s)) => Item::ok("Clock", format!("within {} s of GitHub's clock", s.abs())),
        (None, None) => Item::warn("Clock", "cannot tell whether it is right", FIX),
    }
}

/// None: the lookup did not finish in time.
fn dns_item(result: Option<Result<usize, String>>) -> Item {
    const FIX: &str = "check the nameserver in /etc/resolv.conf (resolvectl status); test it: getent hosts github.com";
    match result {
        Some(Ok(n)) => Item::ok("DNS", format!("github.com resolves ({n} address{})", if n == 1 { "" } else { "es" })),
        Some(Err(e)) => Item::error("DNS", format!("cannot resolve github.com: {e}"), FIX),
        None => Item::error("DNS", "github.com did not resolve in time", FIX),
    }
}

/// Any answer shows the way is open (ghcr.io/v2/ says 401 to everyone); consequence is what
/// breaks without it.
fn outbound_item(label: &str, host: &str, consequence: &str, result: Option<Reached>) -> Item {
    let fix = format!("check the hoster's outbound firewall, DNS and any proxy; test it: curl -sSI https://{host}");
    match result {
        Some(Ok(p)) => Item::ok(label, format!("{host} answers (HTTP {})", p.status)),
        Some(Err(e)) => Item::error(label, format!("cannot connect to {host}: {e}; {consequence}"), fix),
        None => Item::error(label, format!("no answer from {host} in time; {consequence}"), fix),
    }
}

fn disk_item(free: Option<u64>) -> Item {
    let Some(bytes) = free else { return Item::warn("Disk", "cannot tell the free space", "df -h /opt/mikan") };
    let detail = format!("{:.1} GB free in {DIR}", bytes as f64 / (1024.0 * MIB as f64));
    if bytes < 500 * MIB {
        Item::error("Disk", detail, "free space: docker image prune -a removes unused images; old files in /opt/mikan/backups")
    } else if bytes < 2048 * MIB {
        Item::warn("Disk", detail, "free space before the next update: docker image prune -a; old files in /opt/mikan/backups")
    } else {
        Item::ok("Disk", detail)
    }
}

fn memory_item(available_mb: Option<u64>) -> Item {
    match available_mb {
        Some(mb) if mb < 100 => Item::warn("Memory", format!("{mb} MB available"), "add swap or memory; docker stats shows what uses it"),
        Some(mb) => Item::ok("Memory", format!("{mb} MB available")),
        None => Item::warn("Memory", "cannot tell", "free -m"),
    }
}

/// The hello of a node to its panel, tried once more now; the file's last result when that
/// cannot be done.
fn contact(port: Option<u16>, engine: bool) -> Vec<Item> {
    const LABEL: &str = "Panel contact";
    if !hello::path().exists() {
        return vec![Item::ok(LABEL, "this node version does not report it; arikan update")];
    }
    let fresh = engine
        .then(|| capture(docker::compose(&["exec", "-T", "node", "/usr/local/bin/arikan-node", "hello"]), Duration::from_secs(30)))
        .flatten()
        .and_then(|text| hello::parse(text.trim()).or_else(|| text.lines().rev().find_map(|l| hello::parse(l.trim()))));
    let (h, earlier) = match fresh {
        Some(h) => (h, None),
        None => match hello::last() {
            Some(h) => {
                let at = h.at.clone();
                (h, Some(at))
            }
            None => return vec![Item::warn(LABEL, "the node's report cannot be read", "arikan logs node")],
        },
    };
    let mut items = Vec::new();
    if let Some(at) = earlier {
        items.push(Item::warn(LABEL, format!("cannot ask the node now; its last result, from {at}:"), "arikan logs node"));
    }
    items.extend(hello::lines(&h, port).into_iter().map(|(level, text)| Item { level, label: LABEL.into(), detail: text, fix: None }));
    items
}

/// A command's stdout; None when it cannot be run or does not finish in time. Its exit
/// status does not matter.
fn capture(mut cmd: Command, limit: Duration) -> Option<String> {
    let mut child = cmd.stdin(Stdio::null()).stdout(Stdio::piped()).stderr(Stdio::null()).spawn().ok()?;
    let mut pipe = child.stdout.take()?;
    let reader = thread::spawn(move || {
        let mut text = String::new();
        let _ = pipe.read_to_string(&mut text);
        text
    });
    let start = Instant::now();
    loop {
        match child.try_wait() {
            Ok(Some(_)) => return reader.join().ok(),
            Ok(None) if start.elapsed() < limit => thread::sleep(Duration::from_millis(100)),
            _ => {
                let _ = child.kill();
                let _ = child.wait();
                return None;
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn port80_tells_whether_the_certificate_can_come() {
        assert_eq!(port80_item(None, None).level, Level::Ok);
        let gone = port80_item(None, Some("127.0.0.1:18080"));
        assert_eq!(gone.level, Level::Warn);
        assert!(gone.detail.contains("127.0.0.1:18080"), "{}", gone.detail);
        let bare = port80_item(Some("nginx"), None);
        assert_eq!(bare.level, Level::Warn);
        assert!(bare.fix.as_deref().is_some_and(|f| f.starts_with("arikan cert proxy")), "{bare:?}");
        let passed = port80_item(Some("nginx"), Some("127.0.0.1:18080"));
        assert_eq!(passed.level, Level::Ok);
        assert!(passed.detail.contains("nginx holds it and passes"), "{}", passed.detail);
        assert_eq!(port80_item(Some("apache2"), None).level, Level::Warn);
    }

    const UFW: &str = "Status: active

To                         Action      From
--                         ------      ----
22/tcp                     ALLOW       Anywhere
31234/tcp                  ALLOW       203.0.113.5
443                        ALLOW       Anywhere
20000:20100/udp            ALLOW       Anywhere
80,8080/tcp                ALLOW       Anywhere
8443/tcp (v6)              ALLOW       Anywhere (v6)
2053/tcp                   DENY        Anywhere
2087/tcp                   ALLOW OUT   Anywhere
3000/tcp                   ALLOW IN    Anywhere on eth0
OpenSSH                    ALLOW       Anywhere
Anywhere                   ALLOW       10.0.0.0/8
";

    fn want(lo: u16, hi: u16, proto: Option<Proto>, anyone: bool) -> Want {
        Want { what: "test".into(), lo, hi, proto, anyone, level: Level::Error }
    }

    #[test]
    fn the_rules_of_ufw_status() {
        let rules = parse_ufw(UFW);
        let has = |lo, hi, proto, anywhere| rules.contains(&Rule { lo, hi, proto, anywhere });
        assert!(has(22, 22, Some(Proto::Tcp), true));
        assert!(has(31234, 31234, Some(Proto::Tcp), false), "a rule for one address is kept, marked");
        assert!(has(443, 443, None, true));
        assert!(has(20000, 20100, Some(Proto::Udp), true));
        assert!(has(80, 80, Some(Proto::Tcp), true) && has(8080, 8080, Some(Proto::Tcp), true));
        assert!(has(8443, 8443, Some(Proto::Tcp), true), "the (v6) mark is not part of the port");
        assert!(has(3000, 3000, Some(Proto::Tcp), true));
        assert!(has(1, 65535, None, false));
        assert!(!rules.iter().any(|r| r.lo == 2053), "a deny rule opens nothing");
        assert!(!rules.iter().any(|r| r.lo == 2087), "outgoing rules open nothing");
        assert_eq!(parse_ufw("Status: inactive\n"), Vec::new());
        assert_eq!(parse_to("esp"), Vec::new());
        assert_eq!(parse_to("70000"), vec![]);
    }

    #[test]
    fn which_ports_are_open() {
        let rules = parse_ufw(UFW);
        let tcp = Some(Proto::Tcp);
        let udp = Some(Proto::Udp);
        assert!(allowed(&rules, &want(443, 443, tcp, true)), "443 without a protocol is both");
        assert!(allowed(&rules, &want(443, 443, udp, true)));
        assert!(allowed(&rules, &want(443, 443, None, true)));
        assert!(allowed(&rules, &want(20000, 20100, udp, true)));
        assert!(allowed(&rules, &want(20050, 20060, udp, true)));
        assert!(!allowed(&rules, &want(20000, 20100, tcp, true)), "the range is udp only");
        assert!(!allowed(&rules, &want(20000, 20101, udp, true)), "one port outside the range");
        assert!(allowed(&rules, &want(20000, 20100, None, true)), "a rule of either protocol serves an unknown one");
        // the API port may be open for the panel's address alone, an inbound may not
        assert!(allowed(&rules, &want(31234, 31234, tcp, false)));
        assert!(!allowed(&rules, &want(31234, 31234, tcp, true)));
        assert!(!allowed(&rules, &want(2053, 2053, tcp, true)));
        assert!(!allowed(&rules, &want(9999, 9999, None, true)));
        assert!(!allowed(&[], &want(443, 443, None, false)));
        // two rules side by side cover a range together
        let split = parse_ufw("5000:5009/tcp ALLOW Anywhere\n5010:5019/tcp ALLOW Anywhere\n");
        assert!(allowed(&split, &want(5000, 5019, tcp, true)));
    }

    #[test]
    fn the_wishes_of_a_node() {
        let state: NodeState = serde_json::from_str(
            r#"{"inbounds":[
                {"name":"vision","listen":"0.0.0.0","port":"443","config":{"type":"vless"}},
                {"name":"hy2","listen":"::","port":"20000-20100","config":{"type":"hysteria2"}},
                {"name":"hy2 again","port":"443","config":{"type":"hysteria2"}},
                {"name":"inner","listen":"127.0.0.1","port":"9999"},
                {"name":"old","port":"8443","preset":"tuic_v5"},
                {"name":"odd","port":"nothing"}
              ],"relay":{"port":"31000","config":{}},"slots":[]}"#,
        )
        .unwrap();
        let w = wants(Some(31234), None, Some(&state));
        let rules: Vec<String> = w.iter().map(Want::rule).collect();
        assert_eq!(rules, ["31234/tcp", "443/tcp", "20000:20100/udp", "443/udp", "8443/udp", "31000"]);
        assert!(!w[0].anyone && w[1].anyone);
        assert_eq!((w[0].level, w[1].level), (Level::Error, Level::Warn));
        assert_eq!(w[5].commands(), ["ufw allow 31000"]);
        assert_eq!(want(2000, 2010, None, true).commands(), ["ufw allow 2000:2010/tcp", "ufw allow 2000:2010/udp"]);
        // a panel wants its own port, for everyone
        let p = wants(None, Some(21355), None);
        assert_eq!((p.len(), p[0].rule(), p[0].anyone), (1, "21355/tcp".to_owned(), true));
        assert!(wants(None, None, None).is_empty());
        assert_eq!(parse_ports("20000-20100"), Some((20000, 20100)));
        assert_eq!(parse_ports("20000:20100"), Some((20000, 20100)));
        assert_eq!(parse_ports("20100-20000"), None);
        assert_eq!(parse_ports("0"), None);
    }

    #[test]
    fn the_firewall_verdicts() {
        let ok = firewall_items(UFW, &wants(Some(31234), None, None));
        assert_eq!(ok.len(), 1);
        assert_eq!((ok[0].level, ok[0].detail.as_str()), (Level::Ok, "open: 31234/tcp"));

        let plain = "Status: active\n\n22/tcp ALLOW Anywhere\n";
        let items = firewall_items(
            plain,
            &[want(31235, 31235, Some(Proto::Tcp), false), Want { level: Level::Warn, ..want(2053, 2053, None, true) }],
        );
        assert_eq!(items.len(), 2);
        assert_eq!(items[0].level, Level::Error);
        assert!(items[0].detail.contains("31235/tcp (test)"), "{}", items[0].detail);
        assert_eq!(items[0].fix.as_deref(), Some("ufw allow 31235/tcp"));
        assert_eq!((items[1].level, items[1].fix.as_deref()), (Level::Warn, Some("ufw allow 2053")));
        assert_eq!(items[0].lines()[1], "    fix: ufw allow 31235/tcp");
        assert_eq!(firewall_items("Status: active\n", &[]).len(), 1);
    }

    // The node's API port: a rule for everyone, or one for an address the panel left, is
    // reported with the command that puts it right.
    // The node's page: where it answers (and the loopback port a REALITY dest may name), or
    // why it does not.
    #[test]
    fn the_nodes_page_is_reported() {
        let raw = r#"{"listen":"0.0.0.0:443","port":0,"ok":true}"#;
        let site: SiteState = serde_json::from_str(raw).unwrap();
        let item = site_lines(&site);
        assert_eq!((item.level, item.detail.as_str()), (Level::Ok, "https://0.0.0.0:443"));

        let raw = r#"{"listen":"127.0.0.1:18443","port":18443,"ok":true}"#;
        let item = site_lines(&serde_json::from_str(raw).unwrap());
        assert!(item.detail.contains("a REALITY dest may name 127.0.0.1:18443"), "{}", item.detail);

        let raw = r#"{"listen":":443","ok":false,"error":"listen tcp :443: bind: address already in use"}"#;
        let item = site_lines(&serde_json::from_str(raw).unwrap());
        assert_eq!(item.level, Level::Warn);
        assert!(item.detail.contains("address already in use"), "{}", item.detail);
        assert!(item.fix.as_deref().unwrap_or("").contains("MIKAN_SITE_LISTEN"));
        // Every field but the error has a default: an older or a damaged file still reads.
        let item = site_lines(&serde_json::from_str("{}").unwrap());
        assert_eq!(item.level, Level::Warn);
    }

    #[test]
    fn the_node_api_port_is_reported_when_it_is_not_the_panels_alone() {
        let ip = |s: &str| s.parse().unwrap();
        let panel = ip("203.0.113.7");
        let scoped = "Status: active\n\n25305/tcp ALLOW 203.0.113.7\n80/tcp ALLOW Anywhere\n";
        let now = 1_800_000_000;

        // Right as it should be: the panel's address, seen a minute ago.
        assert!(api_port_items(scoped, 25305, &[panel], Some(now - 60), &[panel], now).is_empty());
        // The panel also comes from IPv6 now, and the rule has the v4 address: still fine.
        assert!(api_port_items(scoped, 25305, &[panel, ip("2001:db8::1")], Some(now - 60), &[panel], now).is_empty());
        // Open to everyone.
        let open = "Status: active\n\n25305/tcp ALLOW Anywhere\n";
        let items = api_port_items(open, 25305, &[panel], Some(now - 60), &[], now);
        assert_eq!(items.len(), 1);
        assert_eq!(items[0].level, Level::Warn);
        assert!(items[0].detail.contains("open to everyone"), "{}", items[0].detail);
        assert_eq!(items[0].fix.as_deref(), Some("ufw delete allow 25305/tcp; ufw allow from 203.0.113.7 to any port 25305 proto tcp"));
        // The rule is for an address the panel left.
        let items = api_port_items(scoped, 25305, &[ip("198.51.100.9")], Some(now - 60), &[panel], now);
        assert_eq!(items.len(), 1);
        assert!(items[0].detail.contains("the panel connects from 198.51.100.9"), "{}", items[0].detail);
        // The panel has not been seen for an hour: the node may be out of its reach.
        let items = api_port_items(scoped, 25305, &[panel], Some(now - 3600), &[panel], now);
        assert_eq!(items.len(), 1);
        assert!(items[0].detail.contains("has not reached this node"), "{}", items[0].detail);
        assert_eq!(items[0].fix.as_deref(), Some("ufw allow 25305/tcp"));
        // No rule at all for the port: the check for missing rules speaks, not this one.
        assert!(api_port_items("Status: active\n\n80/tcp ALLOW Anywhere\n", 25305, &[panel], None, &[], now).is_empty());
    }

    #[test]
    fn custom_addons_need_the_bridge_open() {
        let rule = "172.18.0.1 21355/tcp on mikan-addons ALLOW       Anywhere";
        let open = format!("Status: active\n\n22/tcp ALLOW Anywhere\n{rule}\n");
        assert!(addons_item(&open, "").is_none(), "no custom addon runs");
        assert_eq!(addons_item(&open, "172.18.0.1:21355").unwrap().level, Level::Ok);
        let closed = addons_item("Status: active\n\n22/tcp ALLOW Anywhere\n", "172.18.0.1:21355").unwrap();
        assert_eq!(closed.level, Level::Warn);
        assert_eq!(closed.fix.as_deref(), Some("ufw allow in on mikan-addons to 172.18.0.1 port 21355 proto tcp comment mikan-addons"));
        // the rule of the gateway before it moved does not open the new one
        assert_eq!(addons_item(&open, "172.19.0.1:21355").unwrap().level, Level::Warn);
    }

    fn panel_install(port: Option<u16>) -> Install {
        let text = match port {
            Some(p) => format!("MIKAN_VERSION=0.5.2.15\nPANEL_PORT={p}\n"),
            None => "MIKAN_VERSION=0.5.2.15\n".to_owned(),
        };
        Install { config: crate::config::Config::from_env_text("/nonexistent", &text), node: false }
    }

    fn dns(level: Level, text: &str) -> net::DomainCheck {
        net::DomainCheck { notes: vec![net::Note { level, text: text.into() }] }
    }

    // The panel's address in the database against what the server serves: the listener
    // (PANEL_PORT), where public DNS sends the domain, and the public address. A restore from
    // another server or `arikan admin migrate --port` leaves the setting and the server
    // apart, and every line that is not fine carries the command that fits them.
    #[test]
    fn the_panel_address_is_compared_with_what_the_server_serves() {
        let a = |domain: &str, host: &str, port: u16| -> Result<Address, Error> {
            Ok(Address { domain: domain.into(), public_host: host.into(), panel_port: port })
        };
        let ip = |s: &str| s.parse::<Ipv4Addr>().unwrap();

        // The setting and the listener agree.
        let items = address_items(&a("", "", 2053), &panel_install(Some(2053)), None);
        assert_eq!(items.len(), 1);
        assert_eq!((items[0].level, items[0].detail.as_str()), (Level::Ok, "2053, as the database says"));

        // The setting was changed after the panel started: every link would use a port
        // nothing answers on, until a restart fits the setting to the listener.
        let items = address_items(&a("", "", 2053), &panel_install(Some(21355)), None);
        assert_eq!(items.len(), 1);
        assert_eq!(items[0].level, Level::Warn);
        assert!(items[0].detail.contains("21355") && items[0].detail.contains("2053"), "{}", items[0].detail);
        let fix = items[0].fix.as_deref().unwrap();
        assert!(fix.contains("arikan restart") && fix.contains("PANEL_PORT=2053"), "{fix}");

        // A database without the port: links are built without one, and the command that
        // sets it names the listener.
        let items = address_items(&a("", "203.0.113.5", 0), &panel_install(Some(21355)), None);
        assert_eq!(items.len(), 1);
        assert_eq!(items[0].level, Level::Warn);
        assert!(items[0].fix.as_deref().unwrap().contains("--port 21355"), "{:?}", items[0].fix);

        // A server whose config.json lost PANEL_PORT cannot listen where the database says.
        let items = address_items(&a("", "", 2053), &panel_install(None), None);
        assert_eq!(items.len(), 1);
        assert_eq!(items[0].level, Level::Error);
        assert!(items[0].fix.as_deref().unwrap().contains("PANEL_PORT=2053"), "{:?}", items[0].fix);

        // The domain every link is built from, against the public resolvers.
        let seen = |level: Level, text: &str| Some((ip("198.51.100.9"), Some(dns(level, text))));
        let items = address_items(
            &a("vpn.example.com", "", 2053),
            &panel_install(Some(2053)),
            seen(Level::Error, "vpn.example.com points to 203.0.113.5, not to this server (198.51.100.9): fix the A record"),
        );
        assert_eq!(items.len(), 2);
        assert_eq!((items[1].level, items[1].label.as_str()), (Level::Error, "Panel domain"));
        assert!(items[1].fix.as_deref().unwrap().contains("198.51.100.9"), "{:?}", items[1].fix);
        let items = address_items(
            &a("vpn.example.com", "", 2053),
            &panel_install(Some(2053)),
            seen(Level::Ok, "vpn.example.com → 198.51.100.9, this server"),
        );
        assert_eq!(items[1].level, Level::Ok);
        // A domain nobody could ask about: said, not passed over.
        let items = address_items(&a("vpn.example.com", "", 2053), &panel_install(Some(2053)), None);
        assert_eq!(items[1].level, Level::Warn);
        assert!(items[1].detail.contains("vpn.example.com"), "{}", items[1].detail);

        // Without a domain, the public address itself must be this server's.
        let own = |dns: Option<net::DomainCheck>| Some((ip("198.51.100.9"), dns));
        let items = address_items(&a("", "198.51.100.9", 2053), &panel_install(Some(2053)), own(None));
        assert_eq!((items[1].level, items[1].detail.as_str()), (Level::Ok, "198.51.100.9 is this server"));
        let items = address_items(&a("", "203.0.113.5", 2053), &panel_install(Some(2053)), own(None));
        assert_eq!(items[1].level, Level::Error);
        assert_eq!(items[1].fix.as_deref(), Some("arikan admin migrate --public-host 198.51.100.9"));

        // An image that cannot read the address says so, with the update that gives it the
        // command.
        let items = address_items(&Err(anyhow::anyhow!("no such command")), &panel_install(Some(2053)), None);
        assert_eq!(items[0].level, Level::Warn);
        assert!(items[0].detail.contains("cannot be read") && items[0].fix.as_deref().unwrap().starts_with("arikan update"), "{items:?}");
    }

    #[test]
    fn items_print_as_lines() {
        assert_eq!(Item::ok("Docker", "engine 28.0").lines(), ["✓ Docker            engine 28.0"]);
        let bad = Item::error("Disk", "0.3 GB free", "free space").lines();
        assert_eq!(bad, ["✗ Disk              0.3 GB free", "    fix: free space"]);
        assert_eq!(Item::warn("Clock", "late", "set it").lines()[0], "! Clock             late");
        // a fine line has no fix under it
        let fine = Item { fix: Some("x".into()), ..Item::ok("A", "b") };
        assert_eq!(fine.lines().len(), 1);
    }

    #[test]
    fn the_clock() {
        // Date headers are read in UTC; the same moment 5 s later is 5 s ahead.
        let now = 784_111_782;
        assert_eq!(skew_secs("Sun, 06 Nov 1994 08:49:37 GMT", now), Some(5));
        assert_eq!(skew_secs("Sun, 06 Nov 1994 08:49:37 GMT", now - 100), Some(-95));
        assert_eq!(skew_secs("yesterday", now), None);

        assert_eq!(clock_item(Some(true), Some(2)).level, Level::Ok);
        assert_eq!(clock_item(Some(true), None).level, Level::Ok);
        assert_eq!(clock_item(None, Some(-3)).level, Level::Ok);
        let late = clock_item(Some(true), Some(-47));
        assert_eq!(late.level, Level::Error);
        assert!(late.detail.contains("47 s behind GitHub's clock"), "{}", late.detail);
        assert_eq!(late.fix.as_deref(), Some("timedatectl set-ntp true"));
        let ahead = clock_item(Some(false), Some(31));
        assert!(ahead.level == Level::Error && ahead.detail.contains("ahead of"));
        assert_eq!(clock_item(Some(false), Some(4)).level, Level::Warn);
        assert_eq!(clock_item(Some(false), None).level, Level::Warn);
        assert_eq!(clock_item(None, None).level, Level::Warn);
        assert_eq!(clock_item(None, Some(29)).level, Level::Ok);
    }

    #[test]
    fn the_outside() {
        let answered = |status| Some(Ok(net::Probe { status, date: None }));
        assert_eq!(outbound_item("GHCR", "ghcr.io", "x", answered(401)).level, Level::Ok, "401 is an answer");
        let down = outbound_item("GitHub", "github.com", "updates cannot be downloaded", Some(Err("connection refused".into())));
        assert_eq!(down.level, Level::Error);
        assert!(down.detail.contains("updates cannot be downloaded") && down.detail.contains("connection refused"));
        assert_eq!(outbound_item("GitHub", "github.com", "x", None).level, Level::Error);
        assert_eq!(dns_item(Some(Ok(2))).level, Level::Ok);
        assert!(dns_item(Some(Ok(1))).detail.contains("1 address)"));
        assert!(dns_item(Some(Err("failed to lookup".into()))).fix.unwrap().contains("resolv.conf"));
        assert_eq!(dns_item(None).level, Level::Error);
    }

    #[test]
    fn disk_and_memory() {
        assert_eq!(disk_item(Some(100 * MIB)).level, Level::Error);
        assert_eq!(disk_item(Some(499 * MIB)).level, Level::Error);
        assert_eq!(disk_item(Some(500 * MIB)).level, Level::Warn);
        assert_eq!(disk_item(Some(2047 * MIB)).level, Level::Warn);
        assert_eq!(disk_item(Some(2048 * MIB)).level, Level::Ok);
        assert_eq!(disk_item(Some(10 * 1024 * MIB)).detail, "10.0 GB free in /opt/mikan");
        assert_eq!(disk_item(None).level, Level::Warn);
        assert_eq!(memory_item(Some(99)).level, Level::Warn);
        assert_eq!(memory_item(Some(100)).level, Level::Ok);
        assert_eq!(memory_item(None).level, Level::Warn);
        assert_eq!(docker_item(Some("28.0.1")).level, Level::Ok);
        assert_eq!(docker_item(None).level, Level::Error);
    }

    #[test]
    fn containers_by_state() {
        let service =
            |state: &str, status: &str| Service { name: "node".into(), state: state.into(), status: status.into(), ..Default::default() };
        assert_eq!(container(&service("running", "Up 3 hours")).level, Level::Ok);
        assert_eq!(container(&service("running", "Up 3 hours (healthy)")).level, Level::Ok);
        assert_eq!(container(&service("running", "Up 5 seconds (health: starting)")).level, Level::Warn);
        let sick = container(&service("running", "Up 2 minutes (unhealthy)"));
        assert_eq!(sick.level, Level::Error);
        assert_eq!(sick.fix.as_deref(), Some("arikan restart; arikan logs node"));
        assert_eq!(container(&service("exited", "Exited (1) 2 minutes ago")).level, Level::Error);
        assert_eq!(container(&service("restarting", "Restarting (1) 5 seconds ago")).level, Level::Error);
        assert_eq!(containers(Ok(Vec::new()))[0].level, Level::Error);
        assert_eq!(containers(Err("no docker".into()))[0].level, Level::Error);
        assert_eq!(containers(Ok(vec![service("running", "Up")])).len(), 1);
    }

    fn install(node: bool) -> Install {
        Install { config: crate::config::Config::from_env_text("/nonexistent", "MIKAN_VERSION=0.5.1.1\n"), node }
    }

    fn report(items: Vec<Item>) -> Report {
        let mut r = Report::default();
        r.add(items);
        r
    }

    // The JSON is what the panel's HostReport reads: these names, these levels, a fix only
    // where something is wrong, the counts of the problems and the id of the request.
    #[test]
    fn the_json_has_the_shape_the_panel_reads() {
        let r = report(vec![
            Item::ok("Docker", "engine 28.0.1"),
            Item::warn("Clock", "late", "timedatectl set-ntp true"),
            Item::error("Disk", "0.3 GB free", "free space"),
            Item { fix: Some("not shown for a fine line".into()), ..Item::ok("A", "b") },
        ]);
        let j = r.json(&install(false), "abc123");
        let v: serde_json::Value = serde_json::to_value(&j).unwrap();
        let mut keys: Vec<&str> = v.as_object().unwrap().keys().map(String::as_str).collect();
        keys.sort_unstable();
        assert_eq!(keys, ["at", "errors", "id", "items", "kind", "version", "warnings"]);
        assert_eq!((v["id"].as_str(), v["kind"].as_str(), v["version"].as_str()), (Some("abc123"), Some("panel"), Some("0.5.1.1")));
        assert_eq!((v["errors"].as_u64(), v["warnings"].as_u64()), (Some(1), Some(1)));
        assert!(clock::parse_rfc3339(v["at"].as_str().unwrap()).is_some(), "at is RFC 3339");
        let items = v["items"].as_array().unwrap();
        let levels: Vec<&str> = items.iter().map(|i| i["level"].as_str().unwrap()).collect();
        assert_eq!(levels, ["ok", "warn", "error", "ok"]);
        assert_eq!(items[0], serde_json::json!({"level": "ok", "label": "Docker", "detail": "engine 28.0.1"}));
        assert_eq!(items[2]["fix"], "free space");
        assert!(items[3].get("fix").is_none(), "a fine line has no fix");
        assert_eq!(install(true).config.get("MIKAN_VERSION"), Some("0.5.1.1"));
        assert_eq!(r.json(&install(true), "").kind, "node");
    }

    // The report leaves the server: no secret is in it, whoever said it (a node's contact lines
    // carry what the container wrote).
    #[test]
    fn the_report_has_no_secrets() {
        let secret = "AbCdEfGhIjKlMnOpQrStUv0123456789";
        let r = report(vec![
            Item::warn(
                "Panel contact",
                format!("the panel said token={secret} and sent vless://u@h:443?pbk={secret} for ms_{secret}"),
                format!("mikan join {secret}; password=hunter2hunter2"),
            ),
            Item::ok("Container node", format!("Up 3 hours  /{secret}/api")),
        ]);
        let text = serde_json::to_string(&r.json(&install(true), "x")).unwrap();
        // A failure names the secret by its place in the list: the test prints no secret either.
        for (i, s) in [secret, "hunter2hunter2", "u@h"].into_iter().enumerate() {
            assert!(!text.contains(s), "secret {i} is in the report");
        }
        assert!(text.contains("the panel said") && text.contains("Container node"), "{text}");
    }

    // The JSON of a run that nobody asked for has an empty id, which no asker takes for its own.
    #[test]
    fn the_report_of_a_plain_run_has_no_id() {
        assert_eq!(report(Vec::new()).json(&install(false), "").id, "");
        assert_eq!(report(Vec::new()).json(&install(false), "").errors, 0);
    }

    #[test]
    fn a_command_with_a_limit() {
        assert_eq!(capture(Command::new("echo"), Duration::from_secs(5)).as_deref(), Some("\n"));
        let mut slow = Command::new("sleep");
        slow.arg("30");
        let t = Instant::now();
        assert_eq!(capture(slow, Duration::from_millis(300)), None);
        assert!(t.elapsed() < Duration::from_secs(5));
        assert_eq!(capture(Command::new("/nonexistent/Arikan-test"), Duration::from_secs(1)), None);
    }
}
