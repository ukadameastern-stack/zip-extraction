<!--
  SHARED DOC — keep this file byte-identical across all three repos:
    classification-service-demo/aidlc-docs/operations/local-docker-images.md
    office-conversion-service-demo/aidlc-docs/operations/local-docker-images.md
    zip-extraction-service-demo/aidlc-docs/operations/local-docker-images.md
  If you edit one, copy the change into the other two.
-->

# Local Docker Images — Cross-Repo Convention

**Status:** active convention · **Last verified:** 2026-06-04

This document explains how local Docker image names are organised across the
three demo repos that make up the document-uploader pipeline:

- **`classification-service-demo`** — the orchestrator. Owns the local
  `docker compose` stack (LocalStack + bootstrap + classify + router + UI) and
  the `--profile pipeline` extension that pulls the downstream stages in.
- **`office-conversion-service-demo`** — the office-convert stage (Office → PDF).
- **`zip-extraction-service-demo`** — the archive/zip-extraction stage (fan-out).

## TL;DR

1. **Every locally-built image name is unique to its repo.** No two repos build
   or reference the same `name:tag`, so building in one repo can never silently
   clobber an image another repo relies on in the same local Docker daemon.
2. **No repo references another repo's image tag directly.** The orchestrator
   (`classification-service-demo`) consumes the downstream stages through
   **classification-owned aliases**, created by an explicit retag step — never
   by hard-coding a sibling's tag in its compose file.

## Why this matters

All three stacks run against **one shared local Docker daemon**. If two repos
both built (say) `ui:dev` or `worker:dev`, whichever you built last would win
and the other repo's `docker compose up` would silently run the wrong image.
Unique, repo-scoped names make that impossible.

## Image ownership

Each image name is **built by exactly one repo**. That repo owns the name.

| Repo | Locally-built images (it owns these names) |
|------|--------------------------------------------|
| **classification-service-demo** | `classification-service-lambda:dev`, `classification-service-http:dev`, `classification-service-convert-worker:dev`, `ingestion-subgraph:dev`, `document-uploader-ui:dev` |
| **office-conversion-service-demo** | `office-convert:dev`, `office-convert:go`, `office-convert:test`, `office-convert-ui:dev`, `office-convert-smoke-words:dev` |
| **zip-extraction-service-demo** | `zip-extraction-service:dev` |

Third-party images (`localstack/localstack`, `amazon/aws-cli`,
`ghcr.io/wundergraph/cosmo/router`, …) are pulled, not built, and are out of
scope for this convention.

## How cross-repo consumption works locally

`classification-service-demo`'s `--profile pipeline` stack runs the
office-convert and zip-extraction stages. It does **not** name their tags in its
compose file. Instead:

1. **Build the stage images in their own repos** (each produces a repo-owned
   tag):

   ```bash
   # office-convert  ->  office-convert:go
   cd office-conversion-service-demo && make build-go

   # zip-extraction  ->  zip-extraction-service:dev
   cd zip-extraction-service-demo/services/zip-extraction \
     && docker compose -f deploy/docker-compose.yml build zip-extraction
   ```

2. **Bridge them into the classification-owned namespace.** The orchestrator's
   `units/classification-service/scripts/pipeline-images.sh` (run it via
   `make pipeline-images`) retags — a cheap, copy-free alias of the same image
   ID — the sibling images:

   | Sibling-owned source | → | Classification-owned alias |
   |----------------------|---|----------------------------|
   | `office-convert:go` | → | `classification-pipeline/office-convert:local` |
   | `zip-extraction-service:dev` | → | `classification-pipeline/zip-extraction:local` |

3. **Run the pipeline.** The compose services reference only the
   `classification-pipeline/*` aliases (with `pull_policy: never`):

   ```bash
   cd classification-service-demo/units/classification-service
   make pipeline-images
   docker compose --profile pipeline up
   ```

The retag script is the **single, explicit place** the cross-repo coupling
lives. The source tags are overridable via env vars
(`OFFICE_CONVERT_SRC_IMAGE`, `ZIP_EXTRACTION_SRC_IMAGE`) and the destination
aliases via `PIPELINE_OFFICE_CONVERT_IMAGE` / `PIPELINE_ZIP_EXTRACTION_IMAGE`.

## Rules when adding a new repo or a new local stage

1. **Pick a repo-unique image name.** Prefix it with your service/domain
   (e.g. `my-stage-service:dev`). Never reuse a generic tag like `worker:dev`,
   `app:latest`, or another repo's name.
2. **Always set an explicit `image:` in your compose service.** A `build:`
   block with no `image:` defaults to `<compose-project>_<service>`, which is
   non-obvious and collision-prone — pin the name yourself.
3. **Never hard-code a sibling repo's tag.** If your stack needs another repo's
   image, consume it through your own alias + an explicit retag step (mirror
   `pipeline-images.sh`), so the coupling lives in one documented place.
4. **Keep this doc in sync** in all three repos and update the ownership table.
