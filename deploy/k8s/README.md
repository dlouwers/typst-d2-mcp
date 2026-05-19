# Home-lab deployment

Kubernetes manifests for running `typst-d2-mcp` on a k3s + Traefik +
cert-manager + Longhorn + grafana-operator + sealed-secrets cluster.
Mirrors the layout of [`investor-buddy`](https://github.com/dlouwers/investor-buddy)'s
`deploy/k8s/` so the operator playbook is identical.

```
deploy/k8s/
├── base/           # env-agnostic resources (deployment, svc, ingress, …)
├── overlays/
│   ├── prod/       # typst-d2-mcp.stormlantern.nl, pinned semver image
│   └── test/       # test.typst-d2-mcp.stormlantern.nl, Flux-bumped tag
└── dashboard/      # GrafanaDashboard CR + JSON, included by test only
```

## First-time setup

Once per environment (prod and test are separate):

1. **DNS** — add an A record for the hostname pointing at your
   ingress controller.

2. **GitHub OAuth app** — <https://github.com/settings/developers> →
   New OAuth App. Callback URL must be **`https://<host>/auth/github/callback`**.
   Copy the Client ID and a fresh Client Secret.

3. **Metrics bearer** — generate a random token Prometheus will use
   to scrape `/metrics`:

   ```sh
   openssl rand -hex 32
   ```

4. **Seal the Secret.** Start from the `.example` template:

   ```sh
   cp overlays/prod/sealed-secret.yaml.example /tmp/raw-secret.yaml
   $EDITOR /tmp/raw-secret.yaml                          # paste values
   kubeseal --controller-namespace kube-system \
            --format yaml \
            < /tmp/raw-secret.yaml \
            > overlays/prod/sealed-secret.yaml
   shred -u /tmp/raw-secret.yaml
   ```

   The resulting `sealed-secret.yaml` is committable. The bitnami
   controller binds the ciphertext to (namespace, name), so the prod
   and test seals are NOT interchangeable — repeat for both
   environments.

5. **Apply**:

   ```sh
   kubectl apply -k deploy/k8s/overlays/prod/
   # or test:
   kubectl apply -k deploy/k8s/overlays/test/
   ```

   cert-manager will issue the TLS cert on first reconcile; check
   `kubectl describe certificate -n typst-d2-mcp typst-d2-mcp-tls`
   if it takes more than a couple of minutes.

## Release flow

### Prod (pinned semver tag)

```sh
git tag v0.1.0 && git push --tags
# wait for the Image workflow to publish ghcr.io/dlouwers/typst-d2-mcp:v0.1.0
$EDITOR deploy/k8s/overlays/prod/kustomization.yaml   # bump newTag
git commit && git push
kubectl apply -k deploy/k8s/overlays/prod/
```

For ad-hoc rollouts between releases, override locally without
committing:

```sh
kustomize edit set image \
  ghcr.io/dlouwers/typst-d2-mcp=ghcr.io/dlouwers/typst-d2-mcp:sha-abc1234
```

### Test (Flux-managed)

The test overlay's `newTag:` line carries a magic marker comment that
Flux's `image-automation-controller` (in the
[experiments repo](https://github.com/dlouwers/experiments))
rewrites every time a new `<YYYYMMDDHHmmss>-sha-<short>` tag lands in
GHCR. Nothing manual once Flux is wired up.

The marker comment is anchored to the `newTag:` line — **do not
remove or move it**.

## Longhorn migration

Both overlays' `pvc-patch.yaml` switches the data PVC from k3s's
default `local-path` (single-node hostpath) to Longhorn for
replication, snapshots, and backups.

On a **fresh** deploy you can leave `volumeName` commented out and let
Longhorn provision a new PV. The first apply will create the SQLite
file and an empty workspaces directory.

To **migrate existing data** into a specific Longhorn PV (e.g. one
restored from a backup), pin `volumeName: pvc-<uuid>` in the patch and
apply — see [investor-buddy issue #158](https://github.com/dlouwers/investor-buddy/issues/158)
for the busybox-helper data-copy recipe.

## GHCR pull secret (if private)

The image is published to `ghcr.io/dlouwers/typst-d2-mcp` and is
public by default. If you flip the package to private:

1. Create a fine-grained GitHub PAT with `read:packages` scope only.
2. Seal a docker-registry secret named `ghcr-pull`:

   ```sh
   kubectl create secret docker-registry ghcr-pull \
     --docker-server=ghcr.io \
     --docker-username=<your-github-handle> \
     --docker-password=<the-PAT> \
     --dry-run=client -o yaml \
   | kubeseal --controller-namespace kube-system --format yaml \
   > overlays/prod/sealed-ghcr-pull.yaml
   ```

3. Uncomment the `sealed-ghcr-pull.yaml` line in the overlay's
   kustomization.yaml.

## Observability

- **Metrics**: ServiceMonitor scrapes `:9090/metrics` every 30s with a
  bearer token. NetworkPolicy further restricts the port to the
  `monitoring` namespace. The custom metrics are documented in
  `internal/metrics/metrics.go`.
- **Logs**: stdout/stderr only — JSON when `TYPST_D2_MCP_LOG_FORMAT=json`
  (image default). The cluster's log aggregator (Promtail/Loki or
  equivalent) picks them up automatically.
- **Dashboards**: a single GrafanaDashboard CR ships from the test
  overlay and renders both namespaces side-by-side via the
  `namespace` template variable. The JSON lives at
  `dashboard/dashboards/typst-d2-mcp-overview.json` — edit there,
  push, and grafana-operator picks it up within 5 min.

## What this doesn't do (yet)

- **Workspace TTL purge** — sub-issue [#5](https://github.com/dlouwers/typst-d2-mcp/issues/2)
  on the umbrella. Today the per-user workspaces grow without bound;
  the data PVC's `2Gi` request will need raising or a TTL sweeper.
- **Network egress lockdown** — the `typst` child can fetch
  `@preview/*` packages from `typst.app`. For a real public deployment
  add an egress NetworkPolicy (or run the cluster behind a firewall
  that allows only GHCR + Let's Encrypt + GitHub OAuth).
