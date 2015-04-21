{% if grains.network_mode is defined and grains.network_mode == 'calico' %}

include:
  - docker

#/etc/kubernetes/manifests/calico-etcd.manifest:
#  file.managed:
#    - source: salt://calico/calico-etcd.manifest
#    - template: jinja
#    - user: root
#    - group: root
#    - mode: 644
#    - makedirs: true
#    - dir_mode: 755

calico-etcd:
  docker.running:
    - image: quay/etcd:2.0.8
    - command: >
        etcd -name calico
        -advertise-client-urls http://{{grains.api_servers}}:6666
        -listen-client-urls http://0.0.0.0:6666
        -initial-advertise-peer-urls http://{{grains.api_servers}}:6666
        -listen-peer-urls http://0.0.0.0:6666
        -initial-cluster-token calico
        -initial-cluster calico=http://{{grains.api_servers}}:6666
        -initial-cluster-state new
    - ports:
        "6666/tcp":
            HostIp: ""
            HostPort: 6666
    - restart_policy:
        Name: always
    - require:
      - service: docker

{% endif %}