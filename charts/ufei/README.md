# ufei Helm chart

Deploys one `ufei` exporter for an OVN-Kubernetes EgressIP recovery group.

```sh
helm upgrade --install ufei oci://ghcr.io/arch-err/charts/ufei \
  --version 1.0.0 -n team-a -f values.yaml
```

At minimum, configure `probes`. To validate or recover EgressIPs, also configure
`egressIPNames`, a service account with the permissions shown in the
[repository example](https://github.com/arch-err/ufei/blob/main/examples/rbac.yaml),
and pod labels selected by those EgressIPs.

The chart creates a ServiceAccount by default. Set `serviceAccount.name` to the
name referenced by external RBAC, or set `serviceAccount.create=false` to use an
existing account. It also creates namespaced pod-read RBAC by default; disable
that with `rbac.create=false`. Cluster-scoped RBAC remains externally managed.

Recovery is disabled by default. Enable it only when the managed resources are
continuously reconciled by Argo CD with self-heal enabled. See the
[project documentation](https://github.com/arch-err/ufei#readme) for the probe
schema, recovery model, metrics, and complete values example.
