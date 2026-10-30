# Enterprise (on-premises) edition

Banks, media-monitoring firms, government bodies and large retailers will not send data to a shared cloud, and they buy annual contracts with official invoices. The engine is already built for this: a control node, workers added with join tokens, regions, owner isolation and usage metering. This document covers what a sellable on-premises package needs and how to run the first pilots in weeks 9–12.

## 1. What ships

| Part | Form |
|---|---|
| Engine | The `mole` binary (static, linux/amd64 and arm64) with plugins, or the Docker image. Workers join with `mole join` or the install script ([README: Adding worker nodes](../../README.md#adding-worker-nodes)) |
| Control plane | The Laravel app in a "single organisation" mode: no public sign-up, no payment gateway, LDAP or OIDC login (Keycloak is common in Iranian enterprises), teams as departments |
| Dependencies | MySQL 8, S3-compatible storage (MinIO), poppler-utils, Python with Playwright/Chromium on browser workers, an optional OpenAI-compatible LLM endpoint (a self-hosted model is often required) |
| Installer | A Docker Compose bundle for one machine, and an Ansible playbook for clusters. Both must work offline: images, the Chromium download and Python wheels are included, because many sites have no internet access during installation |
| Docs | Install, sizing, backup, upgrade, security hardening; in Persian |

## 2. Gaps to close before the first paid install

1. **Licence key.** A signed file (Ed25519) with customer, expiry, maximum worker nodes and features. Laravel checks it at start and daily; the engine's control node should refuse to issue join tokens beyond the node limit. This needs a small engine change in the cluster join handler; it is not built yet. Keep a grace period of 30 days after expiry so an unpaid invoice never stops a customer's crawls without warning.
2. **Offline updates.** A signed release bundle (binary, plugins, Laravel release, migrations) that an operator uploads. Upgrade order: control node first (it migrates), then workers (they refuse to run another version; see `--allow-version-mismatch`).
3. **Audit log** in Laravel: who created, changed and deleted crawls, schedules, recipes and users.
4. **Data retention and deletion policies** per team, applied with the engine's delete APIs.
5. **Search provider:** inside closed networks, use a self-hosted SearXNG (`search.provider: searxng`) or disable `/v1/search`.

## 3. Sizing (starting points, verify with a load test before quoting)

| Role | CPU / RAM | Notes |
|---|---|---|
| Control node + Laravel + MySQL (small install) | 8 vCPU / 16 GB, SSD | Split MySQL out above a few million pages a month |
| HTTP-only worker | 2 vCPU / 4 GB | Throughput is usually limited by the target sites and `host_rate_limit` (default 2 requests/s per host), not by CPU |
| Browser worker | 4 vCPU / 8 GB per 2–3 Chromium processes (`headless.pool_size`) | Rendering is the main cost; prefer `headless_fallback` over always rendering |
| Storage | MinIO, 3 nodes for redundancy | Screenshots and offloaded bodies (`storage.offload_bodies`) |

## 4. Pricing structure

Setup and integration fee, annual licence by worker count (or by monthly pages), and a support SLA tier (business hours or 24/7), invoiced in Rial with official tax invoices. Custom recipes and plugins are billed as professional services. Include one free "major site change" fix per month per recipe in the SLA.

## 5. Pilots (weeks 9–12)

Goal: two or three organisations running the edition on their own hardware, and a clear answer to "what would you pay for this, and who signs?".

| Week | Work |
|---|---|
| 9 | Shortlist 8–10 organisations (a media-monitoring firm, a bank's market-intelligence unit, a large retailer, a research institute). Book demos with a prepared Persian deck and a live dashboard on their own target sites |
| 10 | Two or three pilot agreements: 6 weeks, free or at setup cost, with written success criteria (sites covered, freshness, accuracy of extracted fields, uptime) |
| 11 | Install with the offline bundle; build each pilot's recipes; daily check-ins |
| 12 | Review against the success criteria; written proposal with price; collect objections (security review, procurement, licence terms) |

Interview questions for every enterprise and shop conversation in weeks 9–12 (also used for the five e-commerce shops):
1. Which sites do you watch today, how, and how many hours a week does it take?
2. What happened the last time you missed a change (a competitor's price, a tender, a news item)?
3. Who else in the company uses this data, and in which tool (Excel, BI, ERP)?
4. Have you tried foreign tools or freelancers? Why did they not stick?
5. Where must the data and the software run, and who approves that?
6. What would you pay a month or a year, and from which budget?

Record answers in one shared sheet. The decision at the end of week 12: lead with SMB price monitoring, enterprise, or both, based on who agreed to pay and how quickly.
