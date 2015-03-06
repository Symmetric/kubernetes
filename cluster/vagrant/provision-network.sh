#!/bin/bash

# Copyright 2014 Google Inc. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

DOCKER_BRIDGE=calico0
NETWORK_CONF_PATH=/etc/sysconfig/network-scripts/
POST_NETWORK_SCRIPT_DIR=/kubernetes-vagrant
POST_NETWORK_SCRIPT=${POST_NETWORK_SCRIPT_DIR}/network_closure.sh


# ensure location of POST_NETWORK_SCRIPT exists
mkdir -p $POST_NETWORK_SCRIPT_DIR

# generate the post-configure script to be called by salt as cmd.wait
cat <<EOF > ${POST_NETWORK_SCRIPT}
#!/bin/bash

set -e

# Only do this operation once, otherwise, we get docker.service files output on disk, and the command line arguments get applied multiple times
grep -q ${DOCKER_BRIDGE} /etc/sysconfig/docker || {
  # modify the docker service file such that it uses the calico docker bridge and not its own
  echo "OPTIONS='-b=${DOCKER_BRIDGE} --iptables=false --selinux-enabled ${DOCKER_OPTS}'" >/etc/sysconfig/docker
  systemctl daemon-reload
  systemctl restart docker.service
}
EOF

chmod +x ${POST_NETWORK_SCRIPT}
