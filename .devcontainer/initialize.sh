#!/bin/sh
# Runs on the HOST (initializeCommand), before the container starts.
#
# Keeps a bare mirror of the private stormlantern-design-system module up
# to date, which devcontainer.json mounts readonly and post-create.sh
# points git at. The Go toolchain in the container then resolves the
# module from the mirror and never needs a GitHub credential of its own.
#
# Why this way round: the host's GitHub credential lives in the macOS
# keychain, which a container cannot read. Mounting ~/.config/gh only
# worked when the token sat in hosts.yml in plain text, readable by every
# install hook post-create runs. Here the credential never leaves the
# host, and only host git needs it: no Go, no gh, nothing else to install.
#
# Plain sh, because this runs under whatever the host has.
#
# Never fails the container start. Offline, or without a credential, an
# existing mirror is simply a little stale; a missing one becomes an empty
# directory so the bind mount still has a source, and the first go command
# then fails at module resolution with the reason printed below.
MIRROR="$HOME/.cache/stormlantern-design-system.git"
REMOTE="https://github.com/dlouwers/stormlantern-design-system"

if [ -d "$MIRROR/objects" ]; then
  git -C "$MIRROR" fetch --prune --quiet \
    || echo "WARNING: could not update $MIRROR; using what it already has." >&2
else
  mkdir -p "$(dirname "$MIRROR")"
  rm -rf "$MIRROR"
  if ! git clone --mirror --quiet "$REMOTE" "$MIRROR"; then
    mkdir -p "$MIRROR"
    echo "WARNING: could not clone $REMOTE on the host." >&2
    echo "  go build/vet/test in the container will fail at module resolution." >&2
    echo "  Check that 'git ls-remote $REMOTE' works on the host, then restart the container." >&2
  fi
fi
exit 0
