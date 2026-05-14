---
on:
  schedule: weekly
  workflow_dispatch:

engine: copilot

permissions:
  contents: read
  issues: read
  pull-requests: read

tools:
  edit:
  bash: ["grep", "cat", "find", "diff", "wc", "sort", "head", "tail", "ls", "pwd"]
  github:
    toolsets: [repos, issues, pull_requests]
    min-integrity: none
    allowed-repos:
      - "velero-io/velero"
      - "ceph/ceph-csi"
      - "kubernetes-csi/external-snapshot-metadata"
      - "kubernetes/enhancements"
      - "red-hat-storage/ceph-csi-operator"
  web-fetch:

network:
  allowed:
    - defaults
    - github

safe-outputs:
  create-pull-request:
    title-prefix: "[doc-sync] "
    labels: [documentation]
    protected-files: fallback-to-issue
  create-issue:
    max: 1
    title-prefix: "[code-sync] "
    labels: [documentation]
---

# Documentation & Code Harmonization Check

You are a documentation consistency auditor for a Ceph RBD Changed Block Tracking (CBT) E2E test suite. This repository contains markdown documentation, Slidev presentation slides, and Go test code that all reference upstream GitHub PRs, issues, and design proposals. These references drift out of sync as upstream status changes.

Your job is to audit all files, check upstream status, and produce two outputs:
1. A **pull request** fixing stale markdown documentation
2. An **issue** listing Go code comments/references that need manual review

## Phase 1: Inventory All References

Scan these file types for GitHub PR/issue URLs and status claims:

**Markdown files** (edit directly if stale):
- `CLAUDE.md` — domain knowledge with extensive PR/issue references and status claims
- `README.md` — project overview with Velero integration status table
- `demo/slides.md` — Slidev presentation with feature coverage and reference slides
- `results.md` — test results with ODF sidecar workaround references
- `velero-coverage.md` — Velero design requirement mapping
- `velero-feedback.md` — Velero accommodation requirements
- `k8s-coverage.md` — Kubernetes upstream coverage mapping
- `ocp-setup/README.md` — OpenShift setup guide
- `deploy-snapshot-metadata-sidecar.md` — sidecar deployment guide

**Go files** (report only, do NOT edit):
- `tests/e2e/*.go` — test files with comments referencing PRs and behavior
- `pkg/**/*.go` — library code with upstream references
- `cmd/**/*.go` — CLI tools with references

For each file, extract:
- All URLs matching `github.com/.*/pull/\d+` or `github.com/.*/issues/\d+`
- All `PR #NNNN` or `Issue #NNNN` references with context
- Status claims: "merged", "open", "closed", "in review", "not yet implemented", "proposed", "planned"
- Organization names: check for stale `vmware-tanzu/velero` (should be `velero-io/velero`)

## Phase 2: Verify Upstream Status

For each unique PR/issue reference found, check the current status using GitHub tools:

### Key references to check:
- `velero-io/velero` PRs: #9528, #9716, #9724, #9736
- `velero-io/velero` Issues: #9710, #9715, #9714, #9556
- `ceph/ceph-csi` PRs: #5347, #2900, #1678, #1160, #3651, #2893
- `ceph/ceph-csi` Issues: #5346, #1800, #2190
- `red-hat-storage/ceph-csi-operator` commits referenced

Record for each: current state (open/closed/merged), title, and any labels or milestones.

## Phase 3: Compare and Detect Discrepancies

Check for these categories of drift:

### 3a. Status mismatches
A PR/issue is described as "open" or "in review" in one file but is actually merged/closed upstream, or vice versa. Example: if a file says "PR #9736 (in review)" but it has been merged, that's a mismatch.

### 3b. Stale organization names
Any reference to `vmware-tanzu/velero` should be `velero-io/velero`. Any URL with `github.com/vmware-tanzu/velero` is stale.

### 3c. Contradictory phrasing across files
The same feature described with different status in different files. Example: one file says "not yet implemented" while another implies it works.

### 3d. Test count inconsistencies
Check that test counts mentioned in documentation match each other. Look for numbers like "48 tests" or "50 tests" across files.

### 3e. Terminology mismatches
Same concept described with different terms across files. Ensure consistent use of: "merged", "open", "closed" (not "in review" vs "open (approved)").

### 3f. Unlinked claims
Status claims that lack a URL to the source. Claims about upstream behavior should link to the relevant PR/issue.

## Phase 4: Produce Outputs

### Output 1: Pull Request (markdown fixes only)

Edit ALL stale `.md` files to fix:
- Stale PR/issue statuses (update to match upstream reality)
- Stale org names (`vmware-tanzu` → `velero-io`)
- Test count mismatches
- Contradictory phrasing (pick the correct version based on upstream)
- Add missing URLs to unlinked claims where possible

In the PR description, list every change with:
- File and line
- What was wrong
- What upstream says
- What was changed

### Output 2: Issue (Go code changes needed)

Create ONE issue listing all Go file discrepancies. Format each finding as:

```
### [filename]:[line] — [brief description]
**Current**: [what the code/comment says]
**Upstream**: [what the actual status is]
**Action needed**: [what should change]
```

Group findings by file. If no Go discrepancies found, skip issue creation.

## Important Rules

- **NEVER edit `.go` files** — only report them in the issue
- **DO edit `.md` files** — fix them directly and include in the PR
- When a PR has been merged, update status to "Merged" with the merge date if available
- When an issue has been closed, note whether it was completed or not_planned
- Preserve the existing formatting style of each file (tables, bullet lists, etc.)
- Keep CLAUDE.md's detailed technical explanations intact — only update status claims
- In demo/slides.md, preserve Slidev syntax (v-clicks, mermaid blocks, HTML divs)
- If unsure about a change, err on the side of NOT changing it and mention it in the PR description as needing human review
