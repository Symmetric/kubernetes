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
      - IP: 10.245.1.3
      - ETCD_IP: 10.245.1.2:4001
      - BIRD_SUBNET: 10.246.0.0/16
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

calico-node:
  docker.running:
    - image: paultiplady/calico-node
    - restart_policy:
        Name: always
    - network_mode: host
    - require:
      - service: docker
      - network: calico0
      - docker: calico-node-installed

{% endif %}

