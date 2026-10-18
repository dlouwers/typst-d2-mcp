# Homebrew Tap Configuration - Complete Setup Guide

## ✅ Already Completed

1. **GitHub repository renamed**: `homebrew-markdown2pdf` → `homebrew-tap`
2. **markdown2pdf updated**: `.goreleaser.yml` points to `homebrew-tap`
3. **typst-d2-mcp updated**: `.goreleaser.yml` includes Homebrew configuration
4. **Both repos pushed to GitHub**

## 🔑 How the release writes to the tap

GoReleaser gets `HOMEBREW_TAP_TOKEN` from the release job, not from a secret.
`.github/actions/tap-token` mints it per run:

1. GitHub OIDC → Google Workload Identity Federation, admitted only for this
   repository's `release.yml` on a `v*` tag (or `verify-tap-write.yml`
   dispatched on `main`).
2. Cloud KMS signs the JWT of the tap GitHub App (id 4637258, installed on
   `dlouwers/homebrew-tap` only, Contents: write). The App's private key was
   imported into KMS and cannot be exported; there is no copy in GitHub.
3. The JWT is exchanged for a one-hour installation token scoped to
   `homebrew-tap`, which the job revokes when it finishes.

Configuration is three repository variables, none of them secret:
`TAP_APP_CLIENT_ID`, `TAP_WIF_PROVIDER` and `TAP_KMS_KEY_VERSION`. The GCP side
and key rotation are `scripts/gcp-tap-kms.nu` in `dlouwers/experiments` (#505).
Run **Verify tap write access** from the Actions tab to prove the whole path
without cutting a release.

## 🍺 What the release publishes

GoReleaser writes two **casks** to `dlouwers/homebrew-tap`:
`Casks/typst-d2-prep.rb` and `Casks/typst-d2-mcp.rb` (`homebrew_casks` in
`.goreleaser.yml`). Both install from the same per-platform archive and each
links its own binary; `d2` and `typst` come along as formula dependencies.
The commits are authored as `stormlantern-actions-write[bot]`.

Casks rather than formulae since #162: GoReleaser deprecated `brews` in v2.10.
Casks work on Linuxbrew as well as macOS. The binaries are not notarized, so
the casks clear `com.apple.quarantine` in a postflight step on macOS.

`markdown2pdf.rb` in the same tap belongs to the markdown2pdf project and is
not touched by this repository's releases.

```bash
brew install --cask dlouwers/tap/typst-d2-prep dlouwers/tap/typst-d2-mcp
```

### Moving existing formula installs to the casks

`tap_migrations.json` in the tap maps both names to the casks, so `brew update`
tells an existing user what changed. It does not migrate them: Homebrew does
not auto-install a cask from a third-party tap without `brew trust`. The
README gives the two commands that do it:

```bash
brew uninstall --formula typst-d2-prep typst-d2-mcp
brew install --cask dlouwers/tap/typst-d2-prep dlouwers/tap/typst-d2-mcp
```

Uninstalling the formula first matters: with the formula still installed,
the cask installs but skips linking its binary, and the old formula binary
stays on the PATH.

## 🧪 Testing a release

**Rehearse with an rc tag.** `v2.2.0-rc.1` runs the whole release —
GitHub pre-release, tap token, casks, brew smoke test, image — without moving
prod: the prod ImagePolicy follows only `^vX.Y.Z$`. The casks in the tap do
point at the rc until the real release.

**Brew smoke test.** `brew-smoke.yml` runs after every release, on macOS and
Linux: it installs both casks from the tap, checks the tap and both binaries
report the tag, and on macOS that the binaries are not quarantined. It does
not gate the prod image. Run it by hand for an existing release:

```bash
gh workflow run brew-smoke.yml -f version=v2.2.0
```

## ⚠️ Common Issues

**Issue**: the mint step fails at Google STS with "rejected by the attribute condition"
**Fix**: the job is not `release.yml` on a `v*` tag push or `verify-tap-write.yml` on `main`; that is the provider working as intended

**Issue**: the mint step fails at `asymmetricSign` with 403 or NOT_FOUND
**Fix**: `TAP_KMS_KEY_VERSION` points at a disabled or wrong version; `scripts/gcp-tap-kms.nu status` in `dlouwers/experiments` lists them

**Issue**: GitHub answers the JWT with 401
**Fix**: the KMS version is not a key the App knows; compare `gcp-tap-kms.nu fingerprint` with the key list on the App's settings page
