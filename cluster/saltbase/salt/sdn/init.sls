{% if grains.network_mode is defined and grains.network_mode == 'calico' %}

sdn:
  cmd.wait:
    - name: /kubernetes-vagrant/network_closure.sh
    - watch:
      - pkg: docker-io
      
{% endif %}
