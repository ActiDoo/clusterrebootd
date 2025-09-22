# Product Requirements Document (PRD)

## Cluster Reboot Coordinator

**Ziel:** Ein produktionsreifer, durchgetesteter Daemon, der Kernel‑Reboots in verteilten Umgebungen koordiniert. Er läuft auf **jedem Node**, erzwingt **exklusive Reboots** via **etcd‑Lock**, und erlaubt Reboots **nur bei nachgewiesener Cluster‑Gesundheit** durch ein **konfigurierbares Health‑Script** (Cluster‑Gate). Auslieferung als **deb** und **rpm** Pakete inklusive Systemd‑Integration, Signierung und SBOM.

---

## 1. Hintergrund & Motivation

* Sicherheits‑ und Kernel‑Updates erfordern periodische Reboots. Unkoordinierte Neustarts können **Quorum brechen** oder **letzte Fallback‑Rollen** ausschalten.
* Ziel ist ein **sicheres Rolling Reboot**: maximal **ein Node gleichzeitig**, nur wenn Cluster‑Policies erfüllt sind (z. B. Quorum intakt, mindestens ein Fallback weiterhin aktiv, keine kritischen SLAs verletzt).

---

## 2. Scope

### In Scope

* Daemon pro Node mit etcd v3‑basiertem globalen Lock (Lease/Mutex)
* Pluggable **Health‑Script** als Gate für **Cluster‑Gesundheit**
* Reboot‑Bedarfserkennung:

  * Debian/Ubuntu: Datei `/var/run/reboot-required`
  * RHEL/CentOS/Alma/Rocky: `needs-restarting -r` (exit code auswerten)
  * SUSE/openSUSE: `zypper ps -s`/Plugin, konfigurierbarer Detector
* Konfiguration via YAML + Reload (SIGHUP)
* Systemd‑Service inkl. Journal‑Logging
* Pakete: **.deb** und **.rpm** (postinst, prerm Hooks, Abhängigkeiten)
* Security (TLS, RBAC), Observability (JSON‑Logs, optional Prometheus)
* Tests: Unit, Integration (lokal), E2E (mehrere VMs), Chaos‑/Fault‑Tests
* CI/CD Pipeline (Build, Test, Lint, SBOM, Signierung, Release)

### Out of Scope (v1)

* GUI/Cloud‑Control‑Plane
* Automatische Wartungsfensterplanung
* Nicht‑systemd Init‑Systeme

---

## 3. Zielumgebungen

* **Distros (x86\_64/arm64):**

  * Ubuntu 20.04/22.04/24.04 (deb)
  * Debian 11/12/13 (deb)
  * RHEL 9/10, AlmaLinux 9/10, Rocky 9/10 (rpm)
  * (Optional) SLES 15 SPx / openSUSE Leap (rpm)
* **etcd v3** Cluster (mind. 3 Knoten), TLS empfohlen
* Systemd ≥ 245

---

## 4. Begriffe

* **Cluster‑Health Gate**: durch Script validierte Policies, die Reboot erlauben/verbieten.
* **Quorum**: Mehrheit der stimmberechtigten Knoten eines Subsystems (z. B. etcd, DB, Control‑Plane).
* **Fallback‑Rolle**: Letzte Reserve‑Rolle (z. B. einzig verbleibender Load‑Balancer, letzter Storage‑Gateway).

---

## 5. Stakeholder & Personas

* **SRE/Platform Engineer**: Betreiber, definiert Policies, rollt Updates aus.
* **Security/Compliance**: fordert verifizierbare Reboot‑Kontrolle, Signaturen, SBOM.
* **App‑Teams**: verlangen minimale Downtime; Transparenz via Logs/Metrics.

---

## 6. Hohe Anforderungen (Non‑Functional)

* **Sicherheit**: TLS‑Mutual‑Auth für etcd, principle of least privilege (RBAC), signierte Pakete, reproduzierbare Builds, SBOM (CycloneDX/Syft), Supply‑Chain‑Provenance (SLSA‑Level Ziel ≥ 2).
* **Zuverlässigkeit**: Lock‑Lease‑TTL > Health‑Timeout; selbstheilendes Verhalten bei Prozessabsturz; kein Deadlock nach Crash/Poweroff.
* **Performance**: Idle‑Overhead minimal (<5 MB RSS, <1% CPU außer Check‑Fenstern).
* **Beobachtbarkeit**: strukturierte JSON‑Logs; optional `/metrics` (Prometheus).
* **Portabilität**: kein Distro‑spezifischer Code ohne Feature‑Flags/Detectors.

---

## 7. Funktionale Anforderungen

### 7.1 Reboot‑Detect

* **FR‑RD‑1**: Standard‑Detectors

  * Debian/Ubuntu: existiert Datei `reboot-required` → Reboot nötig.
  * RHEL‑Familie: `needs-restarting -r` → Exit≠0 → Reboot nötig.
* **FR‑RD‑2**: Konfigurierbare Detector‑Kommandos + Pfade.
* **FR‑RD‑3**: Debounce: erneute Prüfung vor Reboot (nach Lock), um Race‑Conditions zu vermeiden.

### 7.2 etcd‑Locking

* **FR‑LK‑1**: Globaler Mutex über etcd v3 `concurrency.Mutex` mit **Lease TTL** (konfigurierbar, Default 90s).
* **FR‑LK‑2**: Retry mit **exponentiellem Backoff + Jitter**.
* **FR‑LK‑3**: Lock wird bis zum Prozess‑Exit/Reboot gehalten; Lease‑Expiry gibt Lock automatisch frei.
* **FR‑LK‑4**: Optionale Lock‑Annotation: Node‑Name, PID, Timestamp als Value.

### 7.3 Cluster‑Health Gate (Script)

* **FR‑HG‑1**: Ausführbares Script (Pfad konfigurierbar), Timeout konfigurierbar.
* **FR‑HG‑2**: Rückgabecode `0` → **Reboot erlaubt**; `!=0` → **verboten**.
* **FR‑HG‑3**: Script muss **Cluster‑weite Policies** prüfen, z. B.:

  * **Quorum‑Schutz**: Reboot nur, wenn betroffene Subsysteme Quorum behalten (z. B. etcd/DB‑Cluster N/2+1 online).
  * **Fallback‑Schutz**: Reboot nur, wenn **mindestens ein Fallback‑Knoten** (definierbar via Label/Hostliste/Rolle) **nicht** im Reboot ist und **healthy** bleibt.
  * **Parallelismus**: Maximal 1 Node (wird zusätzlich durch Lock erzwungen).
  * **SLA/Traffic‑Gates**: z. B. L7‑Error‑Rate < Schwelle, Queue‑Länge < Schwelle.
* **FR‑HG‑4**: Script erhält **Kontext** per ENV (z. B. `NODE_NAME`, `CLUSTER_SIZE`, `FALLBACK_SET`, `ETCD_ENDPOINTS`).
* **FR‑HG‑5**: **Pre‑Check** vor Lock und **Re‑Check** *unter Lock*; beide müssen erfolgreich sein.

### 7.4 Konfiguration

* YAML‑Datei, Felder (Auszug):

  * `node_name`, `reboot_required_detectors` (Liste), `health_script`, `health_timeout_sec`, `health_publish_interval_sec`, `check_interval_sec`, Backoff‑Parameter, etcd‑Endpoints/TLS, `lock_key`, `lock_ttl_sec`, `reboot_command`, **Cluster‑Policies** (`min_healthy_fraction`, `min_healthy_absolute`, `forbid_if_only_fallback_left: true`).
* **FR‑CF‑1**: Validierung beim Start; Fehler → Exit mit Code ≠ 0.
* **FR‑CF‑2**: Reload via SIGHUP (nicht alle Felder hot‑swappable; dokumentieren).

### 7.5 Systemd & OS‑Integration

* **FR‑SD‑1**: Service‑Unit mit `Restart=always` und `After=network-online.target`.
* **FR‑SD‑2**: Journal‑Logs; Rate‑Limit gegen Log‑Spam.
* **FR‑SD‑3**: Post‑reboot Marker (z. B. `/run/clusterrebootd` mit Timestamp).

### 7.6 Observability

* **FR‑OB‑1**: JSON‑Logs mit Feldern: ts, level, node, event, lock\_wait\_ms, health\_status, detector, action.
* **FR‑OB‑2** (optional): Prometheus `/metrics`: `reboots_total`, `health_checks_total{status}`, `lock_acquire_seconds`, `reboot_blocked_total{reason}`.

### 7.7 Sicherheit

* **FR‑SE‑1**: etcd‑TLS (CA/Client‑Cert/Key), konfigurierbar.
* **FR‑SE‑2**: etcd‑RBAC: Zugriff nur auf Prefix `/<namespace>/clusterrebootd/`.
* **FR‑SE‑3**: Paketsignaturen, SBOM, (optional) cosign‑Attestations.

### 7.8 CLI & Exit‑Codes

* `clusterrebootd status|simulate|validate-config|version`.
* Exit‑Codes standardisieren (z. B. 0 ok, 2 config‑fehler, 3 health‑block, 4 lock‑busy …).

### 7.9 Dry‑Run / Safeguards

* `--dry-run`: Keine Reboots ausführen, nur Logs/Metrics.
* **Global Kill‑Switch**: Datei/Key, der Reboots deaktiviert (`/etc/clusterrebootd/disable`).

---

## 8. Health‑Script: Contract & Referenz

### Contract

* **Input**: ENV Variablen (Node, etcd Endpoints, Policies), optional Flags.
* **Output**: Exit‑Code `0/!=0`; stdout/stderr werden geloggt.
* **Timeout**: durch Daemon erzwungen.

### Referenz (Pseudocode)

```bash
# /usr/local/bin/cluster-health-check.sh
# 1) Verify etcd quorum (or other control-plane)
# 2) Ensure at least one fallback role stays up
# 3) Ensure traffic/SLA within thresholds
# 4) Optionally check peer nodes reboot state via etcd keyspace
```

---

## 9. Paketierung (deb/rpm)

### 9.1 Anforderungen

* **Build‑System**: Go ≥ 1.22 (statisch gelinkt), `nfpm` oder `fpm` für Paketbau.
* **Dateilayout (FHS)**:

  * Binär: `/usr/bin/clusterrebootd` (statisch gelinkt; entspricht dem `ExecStart` der systemd-Unit)
  * Config: `/etc/clusterrebootd/config.yaml` als kommentiertes Template (Conffile) plus reserviertes `/etc/clusterrebootd/config.d/`
  * Kill-Switch: `/etc/clusterrebootd/disable` (wird nicht automatisch angelegt)
  * Systemd: `/lib/systemd/system/clusterrebootd.service` (bzw. `/usr/lib/systemd/system` auf rpm-basierten Distros)
  * tmpfiles.d: `/usr/lib/tmpfiles.d/clusterrebootd.conf` stellt `/run/clusterrebootd` mit kontrollierten Rechten bereit
  * Logging: Ausgabe ins Journal (keine eigenen Logfiles per Default)
  * Dokumentation: README & Blueprint unter `/usr/share/doc/clusterrebootd/`
* **Dependencies**:

  * `systemd`
  * Optional: `ca-certificates` (Empfehlung für TLS) sowie vorhandenes `dnf-utils`/`yum-utils` auf RHEL-Systemen für `needs-restarting`
* **Maintainer‑Scripts**:

  * `preinst`/`preinstall`: legt Konfigurationsverzeichnisse an und bewahrt vorhandene Kill-Switch-Dateien
  * `postinst`/`postinstall`: führt `systemd-tmpfiles --create`, ruft `systemctl daemon-reload` (falls verfügbar) auf und weist auf manuelles `systemctl enable --now` hin statt den Dienst automatisch zu starten
  * `prerm`/`preremove` & `postrm`/`postremove`: stoppen den Dienst nur bei Remove, ignorieren Fehler und lassen Konfigurationsdateien bestehen
* **Signierung**:

  * deb: `dpkg-sig`/`debsign`
  * rpm: `rpmsign`
* **SBOM & Provenance**: Syft/CycloneDX, cosign attest

### 9.2 Release Artefakte

* `clusterrebootd_X.Y.Z_amd64.deb`, `arm64.deb`
* `clusterrebootd-X.Y.Z-1.x86_64.rpm`, `aarch64.rpm`
* Checksums (sha256), SBOM, signatures

---

## 10. Sicherheits- & Betriebsrichtlinien

* TLS Keys/Certs: Dateirechte 0600; kein Versand in Logs.
* Least Privilege für etcd‑User.
* No Reboot während **Blackout‑Windows** (Konfig: CRON‑ähnlich `deny_windows`).
* Optional: **Maintenance Windows** erzwingen (allow‑list Zeiten).

---

## 11. Teststrategie & Abnahme (Definition of Done)

### 11.1 Unit‑Tests (≥ 85% für Kernlogik)

* Config‑Parser/Validierung, Backoff, Detector‑Adapter, Lock‑Lifecycle.

### 11.2 Integrationstests

* Fake‑etcd (embed) + echte etcd in Docker.
* Health‑Timeouts, Lock‑Contention, Lease‑Expiry, Crash‑Recovery.

### 11.3 End‑to‑End (E2E)

* Mehrknoten‑Topologien (3/5/7) in VMs (Vagrant/Multipass/KinD + host‑process).
* Szenarien:

  * **E2E‑1**: Gleichzeitige Reboot‑Anforderung auf N Knoten → genau 1 rebootet.
  * **E2E‑2**: Health‑Script blockiert wegen Quorum‑Gefahr → kein Reboot.
  * **E2E‑3**: Einziger Fallback‑Knoten → Block.
  * **E2E‑4**: Prozess‑Kill unter Lock → Lease‑Timeout → anderer Node kann fortfahren.
  * **E2E‑5**: Detector meldet „nicht mehr nötig“ → Abort nach Re‑Check.

### 11.4 Chaos/Fault Injection

* Netzwerkpartition zu etcd, hohe Latenz, Clock‑Skew, Disk‑Full.

### 11.5 Performance/Soak

* 24‑h Run: keine Memory‑Leaks, stabile CPU/RAM.

### 11.6 Compliance

* SBOM generiert, Pakete signiert, Reproduzierbarkeit (deterministischer Build‑Container).

### 11.7 Abnahmekriterien

* Alle Tests grün auf unterstützten Distros/Architekturen.
* Manuelle Abnahme: zwei reale Wartungsfenster mit „canary“ Nodes.
* Dokumentierte Runbooks & Rollback.

---

## 12. Telemetrie & Logging

* Log‑Levels: info/warn/error/debug.
* Korrelation: `lock_session_id`, `lease_id`, `node`, `attempt`.
* Optional OpenTelemetry Traces (v2).

---

## 13. CI/CD Pipeline (Beispiel GitHub Actions)

* Jobs: `lint` → `test` → `build` → `package` → `sbom` → `sign` → `release`.
* Matrix: {ubuntu‑22.04, debian‑12, almalinux‑9} × {amd64, arm64}.
* Artefakte: Pakete, SBOM, Checksums, Signaturen.

---

## 14. Konfiguration – Beispielschema (YAML)

```yaml
node_name: "node-1"
reboot_required_detectors:
  - type: file
    path: "/var/run/reboot-required"             # Debian/Ubuntu
  - type: command
    cmd: ["/usr/bin/needs-restarting", "-r"]     # RHEL‑Familie; non‑zero = reboot needed
    success_exit_codes: [1]
health_script: "/usr/local/bin/cluster-health-check.sh"
health_timeout_sec: 30
check_interval_sec: 60
backoff_min_sec: 5
backoff_max_sec: 60
lock_key: "/cluster/clusterrebootd/lock"
lock_ttl_sec: 90
etcd_endpoints: ["https://etcd-1:2379","https://etcd-2:2379","https://etcd-3:2379"]
etcd_tls:
  enabled: true
  ca_file: "/etc/ssl/etcd/ca.pem"
  cert_file: "/etc/ssl/etcd/client.pem"
  key_file: "/etc/ssl/etcd/client-key.pem"
reboot_command: ["/sbin/shutdown","-r","now","coordinated kernel reboot"]
cluster_policies:
  min_healthy_fraction: 0.6
  min_healthy_absolute: 3
  forbid_if_only_fallback_left: true
  fallback_nodes: ["lb-1","lb-2"]
windows:
  deny: ["Fri 18:00-Mon 08:00", "* 23:00-06:00"]
  allow: ["Tue 22:00-23:00"]
kill_switch_file: "/etc/clusterrebootd/disable"
metrics:
  enabled: false
  listen: "127.0.0.1:9090"
```

---

## 15. Beispiel‑Systemd Unit

```ini
[Unit]
Description=Cluster Reboot Coordinator (etcd + cluster health gate)
After=network-online.target
Wants=network-online.target
StartLimitBurst=5
StartLimitIntervalSec=60

[Service]
User=root
ExecStart=/usr/local/bin/clusterrebootd --config /etc/clusterrebootd/config.yaml
Restart=always
RestartSec=5
KillSignal=SIGTERM
TimeoutStopSec=15

[Install]
WantedBy=multi-user.target
```

---

## 16. Runbooks

* **Rollout**: Canary → Batch‑Rollout; Dry‑Run zuerst; Health‑Script verifizieren.
* **Rollback**: Kill‑Switch setzen, Service stoppen; ggf. vorherige Paketversion.
* **Incident**: Lock hängt? → Lease prüfen, etcd‑Gesundheit prüfen; notfalls Kill‑Switch.

---

## 17. Security Review Checklist

* TLS korrekt konfiguriert? Cert‑Rotation dokumentiert?
* etcd‑RBAC nur auf benötigten Prefix?
* Keine Secrets in Logs? Dateirechte 0600?
* Pakete signiert? SBOM vorhanden? Provenance an Releases angehängt?

---

## 18. Akzeptanzkriterien (Zusammenfassung)

1. Reboots erfolgen **nie** parallel (>1 Node) – verifiziert durch E2E‑Tests.
2. Reboots erfolgen **nur**, wenn Health‑Script beide Gates (pre & under‑lock) freigibt.
3. Quorum wird **nicht** gebrochen; ein definierter **Fallback** bleibt verfügbar.
4. Pakete (deb/rpm) inkl. Systemd werden bereitgestellt, signiert, mit SBOM.
5. CI/CD produziert reproduzierbare Artefakte; Testmatrix grün.

---

## 19. Roadmap (v2+)

* Kubernetes‑Native Integration (cordon/drain/uncordon, PodDisruptionBudgets)
* Web‑Dashboard (Read‑only)
* OTel Tracing & Advanced Policies (Rate‑Limits, Tickets/Change‑IDs)

---

## 20. Deliverables für den KI‑Agenten

* **Quellcode‑Repo** mit:

  * `cmd/clusterrebootd/` (Daemon, Go) – produktionsreif
  * `pkg/` (Config, Detectors, etcd‑Client, Backoff, Logging)
  * `scripts/` (health‑reference, packaging, e2e)
  * `deploy/` (systemd, nfpm.yaml, examples)
  * `tests/` (unit, integration, e2e)
* **Pipelines** (GitHub Actions/GitLab CI) für Build→Test→Package→Sign→Release
* **Pakete**: deb/rpm (amd64/arm64), **signiert**, inkl. SBOM & Checksums
* **Doku**: Install, Konfig, Runbooks, Security Guide, CHANGELOG, Release Notes
* **Testberichte**: Coverage, E2E‑Logs, Chaos‑Report
