#!/usr/bin/env bash

# Retry a download until we get it.
#
# $1 is the URL to download
download-or-bust() {
  local -r url="$1"
  local -r file="${url##*/}"
  rm -f "$file"
  until [[ -e "${1##*/}" ]]; do
    echo "Downloading file ($1)"
    curl --ipv4 -Lo "$file" --connect-timeout 20 --retry 6 --retry-delay 10 "$1"
    md5sum "$file"
  done
}

# Install salt from GCS.  See README.md for instructions on how to update these
# debs.
install-salt() {
  local salt_mode="$1"

  if dpkg -s salt-minion &>/dev/null; then
    echo "== SaltStack already installed, skipping install step =="
    return
  fi

  mkdir -p /var/cache/salt-install
  cd /var/cache/salt-install

  DEBS=(
    libzmq3_3.2.3+dfsg-1~bpo70~dst+1_amd64.deb
    python-zmq_13.1.0-1~bpo70~dst+1_amd64.deb
    salt-common_2014.1.13+ds-1~bpo70+1_all.deb
  )
  if [[ "${salt_mode}" == "master" ]]; then
    DEBS+=( salt-master_2014.1.13+ds-1~bpo70+1_all.deb )
  fi
  DEBS+=( salt-minion_2014.1.13+ds-1~bpo70+1_all.deb )
  URL_BASE="https://storage.googleapis.com/kubernetes-release/salt"

  for deb in "${DEBS[@]}"; do
    if [ ! -e "${deb}" ]; then
      download-or-bust "${URL_BASE}/${deb}"
    fi
  done

  # Based on
  # https://major.io/2014/06/26/install-debian-packages-without-starting-daemons/
  # We do this to prevent Salt from starting the salt-minion
  # daemon. The other packages don't have relevant daemons. (If you
  # add a package that needs a daemon started, add it to a different
  # list.)
  cat > /usr/sbin/policy-rc.d <<EOF
#!/bin/sh
echo "Salt shall not start." >&2
exit 101
EOF
  chmod 0755 /usr/sbin/policy-rc.d

  for deb in "${DEBS[@]}"; do
    echo "== Installing ${deb} =="
    yum localinstall "${deb}"
    echo sleep 5
  done

  rm /usr/sbin/policy-rc.d

  # Log a timestamp
  echo "== Finished installing Salt =="
}

