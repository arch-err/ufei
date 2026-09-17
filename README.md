# ufei

![UFEI — Unfuck EgressIP](assets/banner.png)

`ufei` probes outbound connectivity through OVN-Kubernetes EgressIPs and exports
Prometheus metrics. After repeated timeouts it can delete an explicit allowlist
of EgressIP resources so Argo CD recreates them.

## Deploy

Tagged releases publish the container to `ghcr.io/arch-err/ufei` and the Helm
chart to `oci://ghcr.io/arch-err/charts/ufei`. Install a published chart with:

```sh
helm upgrade --install ufei oci://ghcr.io/arch-err/charts/ufei \
  --version 1.0.0 -n team-a -f values.yaml
```

The release chart references the matching container by digest. Linux amd64 and
arm64 binary archives, the chart archive, and checksums are also attached to the
GitHub Release.

To build and deploy from source instead:

```sh
docker build -t registry.example.com/platform/ufei:1.0.0 .
docker push registry.example.com/platform/ufei:1.0.0
helm upgrade --install ufei charts/ufei -n team-a -f examples/values.yaml
```

Edit `examples/values.yaml` first. The chart creates one Deployment and Service,
plus an optional ServiceMonitor. `examples/rbac.yaml` contains the required
permissions and `examples/egressip.yaml` shows the expected EgressIP shape.

Recovery is disabled by default. Before enabling it, put every managed EgressIP
under Argo CD with automated sync and self-heal enabled. The application only
deletes resources; Argo CD is responsible for recreating them.

## Configuration

Configuration is supplied through environment variables. The Helm values map
directly to these settings.

| Variable | Default | Purpose |
| --- | --- | --- |
| `UFEI_PROBES` | required | JSON array of 1–64 probes |
| `UFEI_TIMEOUT` | `2s` | Default deadline per probe |
| `UFEI_INTERVAL` | `10s` | Delay between completed rounds, plus 0–10% jitter |
| `UFEI_LISTEN_ADDRESS` | `:8080` | Metrics and health listener |
| `UFEI_METRICS_PREFIX` | `ufei` | Prometheus metric namespace (without the separator underscore) |
| `UFEI_EGRESSIP_NAMES` | empty | Comma-separated EgressIP resource allowlist |
| `UFEI_RECOVERY_ENABLED` | `false` | Enable deletion after repeated timeouts |
| `UFEI_FAILURE_THRESHOLD` | `3` | Failed rounds required before recovery |
| `UFEI_MIN_TIMEOUTS` | `1` | Timed-out probes required for a failed round |
| `UFEI_COOLDOWN` | `5m` | Startup grace and minimum recovery interval |
| `UFEI_API_TIMEOUT` | `5s` | Kubernetes API deadline |
| `UFEI_NAMESPACE` | required with EgressIPs | Probe pod namespace |
| `UFEI_POD_NAME` | required with EgressIPs | Probe pod name |

Durations use Go syntax such as `250ms`, `2s`, and `5m`. Recovery requires at
least two probes, `UFEI_MIN_TIMEOUTS >= 2`, and `UFEI_COOLDOWN >= 30s`.

```json
[
  {"name":"https","protocol":"https","url":"https://health.example.com/ready"},
  {"name":"tcp","protocol":"tcp","domain":"health.example.com","port":443},
  {"name":"icmp","protocol":"icmp","ip":"192.0.2.30"}
]
```

Each probe has a unique `name` and a `protocol` of `http`, `https`, `tcp`, or
`icmp`. HTTP probes accept `url`, or `domain`/`ip` with optional `port` and
`path`. TCP requires a destination and `port`; ICMP requires a destination.
Every probe may override the default `timeout`. An HTTP probe may combine `url`
with `ip` to override the dial address while retaining the URL host for Host and
TLS SNI.

HTTP accepts status 200–399 and verifies TLS. TCP checks connection
establishment. ICMP uses `ping`; the chart configures an unprivileged ping group
range and otherwise drops all capabilities.

## Recovery model

Before and immediately before deletion, `ufei` verifies that every allowlisted
resource exists, selects the running pod, and has all requested IPs assigned.
Deletes use UID and resource-version preconditions. Any validation error or
resource change blocks recovery.

An EgressIP timeout is not attributable to a particular source IP. OVN chooses
the path, and timeouts can also come from DNS, firewalls, or the remote service.
Use independent external targets you control and a conservative quorum. All
resources in the configured recovery group are attempted in one recovery pass;
the set of deletes is not atomic.

The service account needs `list` on the named EgressIPs, `get` on its namespace,
and `get` on pods in its namespace. Recovery also needs `delete` on the named
EgressIPs. Without an EgressIP allowlist, `ufei` only probes and the chart does
not mount a service account token.

## Endpoints and metrics

- `/metrics` — Prometheus metrics
- `/healthz` and `/readyz` — process health; outbound failures do not fail them

Core metrics are `ufei_probe_success`, `ufei_probe_total`,
`ufei_probe_duration_seconds`, `ufei_consecutive_timeout_rounds`,
`ufei_egressip_validation_success`, and `ufei_egressip_delete_total`.

## Development

```sh
nix develop
just test
just build
just image
```

The project is licensed under [MIT](LICENSE).
