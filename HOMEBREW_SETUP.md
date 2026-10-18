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

## 🧪 Testing the Setup

### Test markdown2pdf Release

```bash
cd /Users/dirk/Documents/projects/markdown2pdf
git tag -a v1.2.3 -m "Test release"
git push origin v1.2.3
```

This will:
- Build binaries for all platforms
- Create GitHub release
- Push `Formula/markdown2pdf.rb` to `homebrew-tap`

### Test typst-d2-prep Release

```bash
cd /Users/dirk/Documents/projects/typst-d2-mcp
git tag -a v0.1.0 -m "Initial Go release"
git push origin v0.1.0
```

This will:
- Build binaries for all platforms
- Create GitHub release
- Push `Formula/typst-d2-prep.rb` to `homebrew-tap`

## 📦 Final Repository Structure

After both releases, `dlouwers/homebrew-tap` will contain:

```
homebrew-tap/
├── Formula/
│   ├── markdown2pdf.rb
│   └── typst-d2-prep.rb
└── README.md (optional)
```

## 🍺 User Installation Experience

```bash
# One-time tap setup
brew tap dlouwers/tap

# Install either tool
brew install markdown2pdf
brew install typst-d2-prep

# Or both at once
brew install markdown2pdf typst-d2-prep
```

## 🔍 Verification

After creating a release, verify the formula was pushed:

```bash
# Check the tap repo
gh repo view dlouwers/homebrew-tap --web

# Or clone and inspect
git clone https://github.com/dlouwers/homebrew-tap.git
ls -la homebrew-tap/Formula/
```

## ⚠️ Common Issues

**Issue**: the mint step fails at Google STS with "rejected by the attribute condition"
**Fix**: the job is not `release.yml` on a `v*` tag push or `verify-tap-write.yml` on `main`; that is the provider working as intended

**Issue**: the mint step fails at `asymmetricSign` with 403 or NOT_FOUND
**Fix**: `TAP_KMS_KEY_VERSION` points at a disabled or wrong version; `scripts/gcp-tap-kms.nu status` in `dlouwers/experiments` lists them

**Issue**: GitHub answers the JWT with 401
**Fix**: the KMS version is not a key the App knows; compare `gcp-tap-kms.nu fingerprint` with the key list on the App's settings page
