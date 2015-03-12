{% if grains.network_mode is defined and grains.network_mode == 'openvswitch' %}

openvswitch:
  pkg:
    - installed
  service.running:
    - enable: True

sdn:
  cmd.script:
    - source: /kubernetes-vagrant/network_closure.sh
    - require:
      - pkg: docker-io
      - pkg: openvswitch
{% endif %}
