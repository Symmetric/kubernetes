#!/bin/python
import json
import os
import sys
import etcd
from subprocess import check_output, CalledProcessError

ETCD_AUTHORITY_DEFAULT = "127.0.0.1:4001"
ETCD_AUTHORITY_ENV = "ETCD_AUTHORITY"
PROFILE_LABEL = 'CALICO_PROFILE'
ETCD_PROFILE_PATH = '/calico/'


def _create(args):
    pod_name = args[3]
    docker_id = args[4]
    print('Configuring docker container %s' % docker_id)

    try:
        ip = _read_docker_ip(docker_id)
        _delete_docker_interface(docker_id)
        _create_calico_interface(docker_id, ip)
        #profile = _get_calico_profile(pod_name)
        #if profile:
        #  _apply_calico_profile(docker_id, profile)
    except CalledProcessError as e:
        print('Error code %d creating pod networking: %s\n%s' % (
            e.returncode, e.output, e))
        sys.exit(1)

def _read_docker_ip(container_id):
    ip = check_output([
        'docker', 'inspect', '-format', '{{ .NetworkSettings.IPAddress }}',
        container_id
    ])
    # Clean trailing whitespace (expect a '\n' at least).
    ip = ip.strip()

    print('Docker-assigned IP was %s' % ip)
    return ip

def _delete_docker_interface(container_id):
    print('Deleting eth0')

    # Get the PID of the container.
    pid = check_output([
        'docker', 'inspect', '-format', '{{ .State.Pid }}',
        container_id
    ])
    # Clean trailing whitespace (expect a '\n' at least).
    pid = pid.strip()

    # Set up a link to the container's netns.
    print(check_output(['mkdir', '-p', '/var/run/netns']))
    netns_file = '/var/run/netns/' + pid
    if not os.path.isfile(netns_file):
        print(check_output(['ln', '-s', '/proc/' + pid + '/ns/net',
                            netns_file]))

    # Reach into the netns and delete the docker-allocated interface.
    print(check_output(['ip', 'netns', 'exec', pid,
                        'ip', 'link', 'del', 'eth0']))

    # Clean up after ourselves (don't want to leak netns files)
    print(check_output(['rm', netns_file]))

def _create_calico_interface(container_id, ip):
    print('Configuring Calico networking.')
    env = os.environ
    # Append to existing env, to avoid losing PATH etc.
    # TODO-PAT: This shouldn't be hardcoded
    env['ETCD_AUTHORITY'] = '10.245.1.2:6666'
    print(check_output(
        '/home/vagrant/calicoctl container add %s %s ' % (container_id, ip),
        shell=True,
        env=env,
    ))

def _configure_calico_profile(container_id, pod_name):
    etcd_authority = os.getenv(ETCD_AUTHORITY_ENV, ETCD_AUTHORITY_DEFAULT)
    (host, port) = etcd_authority.split(":", 1)
    etcd_client = etcd.Client(host=host, port=int(port))

    profile_name = _get_calico_profile(pod_name, etcd_client)
    _apply_calico_profile(container_id, profile_name, etcd_client)

def _get_calico_profile(pod_name, etcd_client):
    pod_json = etcd_client.read('/api/v1beta3/namespaces/default/pods/' + pod_name)
    pod_dict = json.loads(pod_json)
    pod_labels = pod_dict.get('labels')
    print('Got pod "%s" labels: %s' % (pod_name, pod_labels))
    pod_profile = None
    if pod_labels:
        pod_profile = pod_labels.get(PROFILE_LABEL)
    print('Got pod "%s" profile: %s' % (pod_name, pod_profile))
    return pod_profile

def _apply_calico_profile(container_id, profile_name, etcd_client):
    profile_exists = True
    # try:
    #     etcd_client.read(ETCD_PROFILE_PATH % {'name': profile_name})
    # exept KeyError:
    #     profile_exists = False


if __name__ == '__main__':
    print('Args: %s' % sys.argv)
    mode = sys.argv[1]

    if mode == 'init':
        print('No initialization work to perform')
    elif mode == 'setup':
        print('Executing Calico pod-creation plugin')
        _create(sys.argv)
    elif mode == 'teardown':
        print('No pod-deletion work to perform')
