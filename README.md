# kint-data

Team data sharing for Kint repos: large files live in the Kint OneDrive (SharePoint team site), git keeps only pointers. Built as a [git-lfs custom transfer agent](https://github.com/git-lfs/git-lfs/blob/main/docs/custom-transfers.md) in standalone mode - no LFS server, no shared secrets, per-user Microsoft Entra sign-in.

```
git push   →  kint-data lfs-agent  →  Microsoft Graph  →  OneDrive /lfs-store/objects/…
git lfs pull  ←──────────────────────────────────────────┘
```

## Install

macOS/Linux:

```sh
curl -sfL https://raw.githubusercontent.com/kint-pro/kint-data-cli/main/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/kint-pro/kint-data-cli/main/install.ps1 | iex
```

Requirements: `git`, `git-lfs` ≥ 3.0 (`brew install git-lfs`).

## Use an existing data repo

Order matters - `init` before any `git lfs pull`:

```sh
git clone <repo>
cd <repo>
kint-data init      # wires git-lfs to the kint-data agent (reads committed .kintdata)
kint-data login     # once per machine, device-code sign-in with your @kint.pro account
git lfs pull        # fetch the data
```

Pulling before `init` fails with a cryptic git-lfs HTTPS error - git-lfs tries its default endpoint because the agent is not registered yet.

After that, normal git: `git add` / `commit` / `push` uploads new data automatically, `git lfs pull` downloads it. `kd` is a handy alias.

## Set up a new data repo (project owner)

```sh
git init && cd <repo>
kint-data init \
  --tenant <entra-tenant-id> \
  --client <app-client-id> \
  --remote '<drive-id>:/lfs-store'
git lfs track "data/**"
git add .kintdata .gitattributes && git commit -m "wire kint-data"
```

`.kintdata` carries the shareable settings (IDs only, no secrets) - committing it is what makes `kint-data init` work for teammates. Values for the Kint tenant live in the internal runbook (team drive, not in this repo).

## Commands

| Command | Purpose |
|---|---|
| `kint-data init` | Wire the current repo to the transfer agent (`--tenant/--client/--remote` for first-time setup, `--yes` to confirm a drive change) |
| `kint-data login` | Device-code sign-in, cached locally (`~/.config/kint-data/`, 0600) |
| `kint-data logout` | Remove the local session (offboarding = removal from the kint M365 group) |
| `kint-data doctor` | Check binary, git, git-lfs, repo config, auth, drive reachability |
| `kint-data lfs-agent` | Internal - invoked by git-lfs |

## Admin runbook (once per tenant)

1. **App registration** (public client, no secret): delegated Graph permission `Sites.Selected`, admin consent, `is-fallback-public-client=true`.
2. **Site grant**: `POST /v1.0/sites/{site-id}/permissions` with roles `["write"]` for the app - the token then only reaches this site, nothing else a user can access.
3. **Library hygiene**: cap or disable versioning on the LFS document library (objects are immutable; versions only waste quota) and rely on the site's recycle bin for accidental deletes.
4. **Access control**: membership in the M365 group *is* the ACL. Offboarding = remove from group; local `logout` is cosmetic.
5. Recommended: Conditional Access policy restricting device-code flow to managed devices/MFA.

The exact commands used for the Kint tenant (with rollback steps) are documented in the internal runbook on the team drive.

## Design

- **Remote layout**: content-addressed `/<root>/objects/<oid[0:2]>/<oid[2:4]>/<oid>` - dedupe for free, integrity checked by git-lfs itself after download.
- **Uploads**: ≤ 4 MiB single request; larger via Graph upload sessions (10 MiB chunks, resumable semantics, no torn objects - incomplete sessions are cancelled).
- **Conflicts**: same object pushed twice → skipped; concurrent same-object race → benign; size mismatch (torn remote) → loud repair by replace.
- **Auth**: MSAL device-code flow, silent refresh in the agent, never interactive during `git push`. Token cache is per-user, file-locked against git-lfs's parallel agent processes.
- **CI**: not supported in v0.1 - device-code needs a human.


## Development

```sh
make build   # build ./kint-data
make test    # go test ./...
```

Releases: tag `v*` → GitHub Actions + GoReleaser (darwin/linux/windows, amd64/arm64).
