# TPCall Backend — Deployment

Production: **https://tpcallapi.tafakorpardazan.ir** (health: `/health`, WebSocket: `/api/v1/ws`)

Runs on the k3s cluster at `10.20.0.1` in the `tpcall` namespace, deployed by GitLab CI
(`gitlab.tafakorpardazan.ir/tafakor-pardazan/tpcall-backend`). The setup mirrors the `tpsite` project.

## Architecture

```
client ──HTTPS──> Traefik (kube-system, Let's Encrypt resolver "le")
                     └─> Service backend:9090 ──> Deployment backend (1 replica)
                                                      └─> Service postgres:5432 ──> StatefulSet postgres-0 (PVC 5Gi, local-path)
```

| Object | Name | Notes |
|---|---|---|
| Namespace | `tpcall` | Pod Security: enforce `baseline`, warn `restricted` |
| Deployment | `backend` | Image `gitlab.tafakorpardazan.ir:5050/tafakor-pardazan/tpcall-backend:<commit SHA>`; **1 replica on purpose** (presence/call state is in memory), `Recreate` strategy |
| StatefulSet | `postgres` | `postgres:16-alpine`, PVC `data-postgres-0` (5Gi) |
| Ingress | `tpcall` | Host `tpcallapi.tafakorpardazan.ir`, TLS via certresolver `le` |
| ConfigMap | `tpcall-config` | Non-secret settings (see below) |
| Secret | `tpcall-secrets` | `POSTGRES_PASSWORD`, `JWT_SECRET`, `DATABASE_URL` (admin-created, not in git) |
| Secret | `gitlab-registry` | Image pull credentials (GitLab deploy token, `read_registry`) |
| NetworkPolicies | default-deny ingress; Traefik → backend:9090; backend → postgres:5432 | |
| ResourceQuota | `tpcall-quota` | 1 CPU / 1Gi requests, 2 CPU / 2Gi limits, 3 PVCs |

Migrations in `migrations/` run automatically when the backend starts.

## Repository layout

```
.gitlab-ci.yml                       pipeline
Dockerfile                           multi-stage Go build
deploy/base/                         quota, configmap, postgres, backend, networkpolicy, ingress
deploy/overlays/production/          image name + tag (tag replaced by CI)
deploy/cluster/tpcall-namespace.yaml admin-applied: namespace + deployer Role/RoleBinding
```

## CI/CD pipeline

Runs only on the default branch (`main`). Stages:

1. **test** (`test:go`, tag `build`) — `go vet ./...`, `go test ./...`
2. **build** (`build:image`, tag `build`) — BuildKit builds the Dockerfile and pushes
   `:<commit SHA>` and `:main` to the GitLab registry (auth with `CI_JOB_TOKEN`).
3. **deploy** (`deploy:production`, tag `deploy`) — sets the image tag to the commit SHA, renders
   `deploy/overlays/production` with kustomize, `kubectl apply`, waits for the rollout and
   **rolls back automatically** if it fails.

Runners: the existing `tpsite-build` (tag `build`) and `tpsite-deploy` (tag `deploy`) runners are
assigned to this project (GitLab Admin → Runners). Both are *protected*, so they only run on
protected branches. Jobs run in pods in the `ci` namespace; deploy uses ServiceAccount
`ci/ci-deploy`, which is bound to Role `tpcall-deployer` in `tpcall` only (no access to secrets).

**Outbound proxy:** the test and build jobs use `http://130.185.123.6:3128` (reachable only from the
server network) for Docker Hub, Go modules and apk. GitLab/registry/cluster addresses bypass it
(`NO_PROXY`). Deploy does not use it.

## Configuration

Non-secret (ConfigMap `tpcall-config`, from `deploy/base/configmap.yaml`):
`HTTP_HOST`, `HTTP_PORT=9090`, `PUBLIC_BASE_URL=https://tpcallapi.tafakorpardazan.ir`,
`JWT_EXPIRATION`, `RING_TIMEOUT`, `LOG_LEVEL`, `CORS_ORIGINS`, `ICE_SERVERS`.

Secret (`tpcall-secrets`, created by an admin from the local `.env` values — never committed):
`POSTGRES_PASSWORD`, `JWT_SECRET`, `DATABASE_URL`.

To change a non-secret value: edit `deploy/base/configmap.yaml`, push to `main`.
(Changing a ConfigMap does not restart pods by itself — run `kubectl -n tpcall rollout restart deploy/backend`.)

To change a secret (run on the server as root):

```bash
kubectl -n tpcall create secret generic tpcall-secrets \
  --from-literal=POSTGRES_PASSWORD='...' \
  --from-literal=JWT_SECRET='...' \
  --from-literal=DATABASE_URL='postgres://tpcall:<POSTGRES_PASSWORD>@postgres:5432/tpcall?sslmode=disable' \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n tpcall rollout restart deploy/backend
```

> Changing `POSTGRES_PASSWORD` only takes effect on first database initialisation. For an existing
> database also run `ALTER USER tpcall PASSWORD '...'` inside `postgres-0`.
> Rotating `JWT_SECRET` logs everyone out.

## One-time setup (already done — for rebuilding from scratch)

1. Apply namespace and RBAC: `kubectl apply -f deploy/cluster/tpcall-namespace.yaml`
2. Create `tpcall-secrets` (see above).
3. Create a GitLab **deploy token** on the project (Settings → Repository → Deploy tokens, scope
   `read_registry`) and create the pull secret:
   ```bash
   kubectl -n tpcall create secret docker-registry gitlab-registry \
     --docker-server=gitlab.tafakorpardazan.ir:5050 \
     --docker-username='<deploy token username>' \
     --docker-password='<deploy token value>' \
     --dry-run=client -o yaml | kubectl apply -f -
   ```
4. GitLab Admin → CI/CD → Runners: assign runners `tpsite-build` and `tpsite-deploy` to the project.
5. DNS: `tpcallapi.tafakorpardazan.ir` → `193.151.152.230` (server public IP).
6. Push to `main`; the first pipeline deploys everything.

## Operations

```bash
kubectl -n tpcall get pods,ingress,pvc
kubectl -n tpcall logs deploy/backend -f
kubectl -n tpcall rollout restart deploy/backend
kubectl -n tpcall rollout undo deploy/backend          # manual rollback
kubectl -n tpcall exec -it postgres-0 -- psql -U tpcall -d tpcall
curl https://tpcallapi.tafakorpardazan.ir/health       # {"status":"ok"} (checks DB too)
```

Database backup (manual — there is **no automated backup yet**):

```bash
kubectl -n tpcall exec postgres-0 -- pg_dump -U tpcall tpcall | gzip > tpcall-$(date +%F).sql.gz
```

## Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| Pod `ErrImagePull` / `insufficient_scope` | `gitlab-registry` secret missing, wrong, or token revoked/expired → recreate it from a valid deploy token of *this* project |
| Pipeline "cannot be run" | YAML error in `.gitlab-ci.yml` (quote script lines containing `: `) |
| Jobs stay pending | Runners not assigned to the project, or branch not protected |
| `go mod` / image pull timeouts in CI | Outbound proxy unreachable; check `PROXY_URL` in `.gitlab-ci.yml` |
| Certificate not issued | DNS must point at the server and port 80 must be reachable (HTTP challenge) |
| Backend crash-loop | `kubectl -n tpcall logs deploy/backend`; usually bad `DATABASE_URL`/`JWT_SECRET` |

## Known limitations

- Single backend replica and single Postgres instance (no HA).
- No automated database backups.
- `CORS_ORIGINS` is `*`; restrict it once client origins are known.
- `ICE_SERVERS` is empty (no STUN/TURN configured) — WebRTC calls across NATs may need one.
