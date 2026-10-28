# Security Policy

## Reporting a Vulnerability

Report privately to **cephcsi-cbt-e2e@tig.pw**. If GitHub private vulnerability
reporting is enabled for this repository, the Security tab works too. Please do not
open a public issue for an unpatched vulnerability.

Include the affected file or script, reproduction steps, impact, and any suggested fix.

## Scope

This repository is an end-to-end test harness and cluster setup toolkit for Ceph RBD
Changed Block Tracking. It is not a deployed service. Reports that are in scope include:

- command or path injection in the shell scripts, or in Go helpers that build shell
  commands such as `pkg/data`
- credentials, kubeconfigs, or cluster secrets written to logs or committed
- CI workflows that could be tricked into running untrusted code

Vulnerabilities in Ceph, CephCSI, Kubernetes, OpenShift, Velero, or the Slidev deck's
dependencies are out of scope here; report them to those projects. Dependabot tracks
the Go modules and the deck's npm packages.

## Supported Versions

Only `main` is supported.
