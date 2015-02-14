{% if grains.network_mode is defined and grains.network_mode == 'calico' %}

include:
  - docker

docker-py:
  pkg.installed:
    - name: python-pip
  pip.installed:
    - reload_modules: True
    - require:
      - pkg: python-pip

paultiplady/calico-node:
  docker.pulled

calico-node-installed:
  docker.installed:
    - name: calico-node
    - image: paultiplady/calico-node
    - environment:
      - IP: {{ grains.minion_ip }}
      - ETCD_IP: {{ grains.etcd_servers}}:4001
      - BIRD_SUBNET: {{ grains['cbr-cidr'] }}
    - require:
      - docker: paultiplady/calico-node

# Workaround for bug https://github.com/saltstack/salt/issues/20570
# Need to manually load kernel module for network.managed state.
bridge:
  kmod.present

calico0:
  network.managed:
    - enabled: True
    - type: bridge
    - ipaddr: {{ grains.container_ip }}
    - netmask: 255.255.255.0
    - require:
      - kmod: bridge

ip6_tables:
  kmod.present

xt_set:
  kmod.present

calico-iptables:
 iptables.append:
   - table: nat
   - chain: POSTROUTING
   - source: 10.246.0.0/16
   - dest: "not 10.246.0.0/16"
   - jump: MASQUERADE

calico-node:
  docker.running:
    - image: paultiplady/calico-node
    - restart_policy:
        Name: always
    - network_mode: host
    - privileged: True
    - require:
      - service: docker
      - network: calico0
      - docker: calico-node-installed
      - kmod: ip6_tables
      - kmod: xt_set
      - iptables: calico-iptables

calico-node-etcd:
  cmd.wait:
    - name: curl -L http://{{ grains.etcd_servers }}:4001/v2/keys/calico/nodes/{{ grains.nodename }} -XPUT -d value={{ grains.minion_ip }}
    - watch:
      - docker: calico-node

{% endif %}

