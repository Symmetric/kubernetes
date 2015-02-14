base:
  '*':
    - base
    - debian-auto-upgrades

  'roles:kubernetes-pool':
    - match: grain
    - docker
    - kubelet
    - kube-proxy
{% if pillar['enable_node_monitoring'] is defined and pillar['enable_node_monitoring'] %}
    - cadvisor
{% endif %}
{% if pillar['enable_node_logging'] is defined and pillar['enable_node_logging'] %}
  {% if pillar['logging_destination'] is defined and pillar['logging_destination'] == 'elasticsearch' %}
    - fluentd-es
  {% endif %}
  {% if pillar['logging_destination'] is defined and pillar['logging_destination'] == 'gcp' %}
    - fluentd-gcp
  {% endif %}
{% endif %}
    - logrotate
{% if grains['cloud'] is defined and grains['cloud'] == 'azure' %}
    - openvpn-client
{% else %}
    - sdn
{% endif %}
{% if grains.network_mode is defined and grains.network_mode == 'calico' %}
    - calico-node
{% endif %}

  'roles:kubernetes-master':
    - match: grain
    - generate-cert
    - etcd
    - kube-apiserver
    - kube-controller-manager
    - kube-scheduler
    - monit
    - nginx
    - kube-client-tools
    - logrotate
    - kube-addons
{% if grains['cloud'] is defined and grains['cloud'] == 'azure' %}
    - openvpn
{% endif %}
{% if grains.network_mode is defined and grains.network_mode == 'calico' %}
    - calico-master
{% endif %}

  'roles:kubernetes-pool-vsphere':
    - match: grain
    - static-routes

  'roles:kubernetes-pool-vagrant':
    - match: grain
    - vagrant
