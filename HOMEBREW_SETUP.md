# Homebrew Tap Configuration - Complete Setup Guide

## ✅ Already Completed

1. **GitHub repository renamed**: `homebrew-markdown2pdf` → `homebrew-tap`
2. **markdown2pdf updated**: `.goreleaser.yml` points to `homebrew-tap`
3. **typst-d2-mcp updated**: `.goreleaser.yml` includes Homebrew configuration
4. **Both repos pushed to GitHub**

## 🔧 Manual Step Required: Add GitHub Secret

GoReleaser needs a GitHub token to push formula files to your `homebrew-tap` repository.

### Option 1: Copy Existing Token (If You Have It)

If you still have the GitHub Personal Access Token you created for markdown2pdf:

```bash
gh secret set HOMEBREW_TAP_TOKEN --repo dlouwers/typst-d2-mcp --body "YOUR_TOKEN_HERE"
```

### Option 2: Use the Same Token via Web UI

1. Go to https://github.com/dlouwers/typst-d2-mcp/settings/secrets/actions
2. Click **New repository secret**
3. Name: `HOMEBREW_TAP_TOKEN`
4. Value: Paste the same token you used for markdown2pdf
5. Click **Add secret**

### Option 3: Create a New Token (If Needed)

1. Go to https://github.com/settings/tokens/new
2. Note: `GoReleaser Homebrew Tap`
3. Expiration: Choose duration (90 days, 1 year, or no expiration)
4. Scopes: Check **`repo`** (full control of private repositories)
5. Click **Generate token**
6. Copy the token (you won't see it again!)
7. Add to both repos:

```bash
# Set for typst-d2-mcp
gh secret set HOMEBREW_TAP_TOKEN --repo dlouwers/typst-d2-mcp --body "ghp_xxxxxxxxxxxx"

# Update for markdown2pdf (if needed)
gh secret set HOMEBREW_TAP_TOKEN --repo dlouwers/markdown2pdf --body "ghp_xxxxxxxxxxxx"
```

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

**Issue**: GoReleaser fails with "403 Forbidden" or "Token not found"
**Fix**: Ensure `HOMEBREW_TAP_TOKEN` is set in the repository secrets

**Issue**: Formula not appearing in tap
**Fix**: Check GoReleaser logs in GitHub Actions, ensure token has `repo` scope

**Issue**: Token expired
**Fix**: Generate new token, update secret in both repos

---

**Status**: ✅ Configuration complete, pending `HOMEBREW_TAP_TOKEN` setup for typst-d2-mcp
