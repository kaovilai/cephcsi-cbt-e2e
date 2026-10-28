# Contributing

## Setup

You need Go (the version in `go.mod`), `make`, and `shellcheck`. A live
Kubernetes/OpenShift cluster with CephCSI and the external-snapshot-metadata sidecar is
only needed for the e2e suite.

## Checks Before a Pull Request

```bash
make build
make test
make test-race
make vet
make lint
make shellcheck
```

Run `npm --prefix demo run build` if you changed `demo/slides.md`. The e2e tests need a
cluster: use `make cluster-*` or `./run-in-cluster.sh`, not the local `make e2e-*`
targets. Say what you could and could not run.

## Conventions

- Sign commits for DCO: `git commit -s`.
- Keep subjects in the existing style: `fix:`, `feat:`, `test:`, `docs:`, `ci:`,
  `build(deps):`, `chore:`.
- When behavior, upstream status, or terminology changes, update the matching docs in
  the same change: `README.md`, `CLAUDE.md`, the coverage documents, and
  `demo/slides.md`.
- Keep new shell logic warning-clean; `make shellcheck` runs at the default level.

## Security

Report vulnerabilities privately as described in [SECURITY.md](./SECURITY.md), not in a
public issue.

## Questions

Open an issue, or email cephcsi-cbt-e2e@tig.pw for matters that do not belong in a
public tracker.
