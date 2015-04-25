#!/bin/python
from collections import namedtuple
import json
import os
import re
import sys
from subprocess import check_output, CalledProcessError
import requests
import sh

# Append to existing env, to avoid losing PATH etc.
# TODO-PAT: This shouldn't be hardcoded
env = os.environ.copy()
env['ETCD_AUTHORITY'] = 'kubernetes-master:6666'
calicoctl = sh.Command('/home/vagrant/calicoctl').bake(_env=env)

ETCD_AUTHORITY_ENV = "ETCD_AUTHORITY"
PROFILE_LABEL = 'CALICO_PROFILE'
ETCD_PROFILE_PATH = '/calico/'
AllowRule = namedtuple('AllowRule', ['port', 'proto', 'source'])


class NetworkPlugin():
    def __init__(self):
        self.pod_name = None
        self.docker_id = None

    def create(self, args):
        """"Create a pod."""
        # Calicoctl only
        self.pod_name = args[3].replace('-', '_')
        self.docker_id = args[4]
        print('Configuring docker container %s' % self.docker_id)

        try:
            self._configure_interface()
            self._configure_profile()
        except CalledProcessError as e:
            print('Error code %d creating pod networking: %s\n%s' % (
                e.returncode, e.output, e))
            sys.exit(1)

    def _configure_interface(self):
        """Configure the Calico interface for a pod."""
        ip = self._read_docker_ip()
        self._delete_docker_interface()
        print('Configuring Calico networking.')
        print(calicoctl('container', 'add', self.docker_id, ip))

    def _read_docker_ip(self):
        """Get the ID for the pod's infra container."""
        ip = check_output([
            'docker', 'inspect', '-format', '{{ .NetworkSettings.IPAddress }}',
            self.docker_id
        ])
        # Clean trailing whitespace (expect a '\n' at least).
        ip = ip.strip()

        print('Docker-assigned IP was %s' % ip)
        return ip

    def _delete_docker_interface(self):
        """Delete the existing veth connecting to the docker bridge."""
        print('Deleting eth0')

        # Get the PID of the container.
        pid = check_output([
            'docker', 'inspect', '-format', '{{ .State.Pid }}',
            self.docker_id
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

    def _configure_profile(self):
        """
        Configure the calico profile for a pod.

        Currently assumes one pod with each name.
        """
        calicoctl('profile', 'add', self.pod_name)
        ports = self._get_pod_ports()
        rules = self._generate_rules(ports)
        self._apply_rules(self.pod_name, rules)

    def _get_pod_ports(self):
        """
        Get the list of ports on containers in the Pod.

        :return list ports: the Kubernetes ContainerPort objects for the pod.
        """
        pods = self._get_pods()

        for pod in pods:
            print('Processing pod %s' % pod)
            if pod['metadata']['name'].replace('-', '_') == self.pod_name:
                named_pod = pod
                break
        else:
            raise KeyError('Pod not found: ' + self.pod_name)
        print('Got pod data %s' % pod)

        ports = []
        for container in named_pod['spec']['containers']:
            try:
                more_ports = container['ports']
                print('Adding ports %s' % more_ports)
                ports.extend(more_ports)
            except KeyError:
                pass
        return ports

    def _get_pods(self):
        """
        Get the list of pods from the Kube API server.

        :return list pods: A list of Pod JSON API objects
        """
        bearer_token = self._get_api_token()
        session = requests.Session()
        session.headers.update({'Authorization': 'Bearer ' + bearer_token})
        response = session.get(
            'https://kubernetes-master:6443/api/v1beta3/pods',
            verify=False,
        )
        response_body = response.text
        # The response body contains some metadata, and the pods themselves
        # under the 'items' key.
        pods = json.loads(response_body)['items']
        print('Got pods %s' % pods)
        return pods

    def _get_api_token(self):
        """
        Get the kubelet Bearer token for this node, used for HTTPS auth.
        :return string: The token.
        """
        with open('/var/lib/kubelet/kubernetes_auth') as f:
            json_string = f.read()
        print('Got kubernetes_auth: ' + json_string)

        auth_data = json.loads(json_string)
        return auth_data['BearerToken']

    def _generate_rules(self, ports):
        """
        Generate the Profile rules that have been specified on the Pod's ports.

        We only create a Rule for a port if it has 'allowFrom' specified.

        The Rule is structured to match the Calico etcd format.

        :param list() ports: a list of ContainerPort objecs.
        :return list() rules: the rules to be added to the Profile.
        """
        rules = []

        if ports:
            for port in ports:
                try:
                    rule = {
                        'action': 'allow',
                        'dst_ports': [port['containerPort']],
                        'src_tag': port['allowFrom']
                    }
                    try:
                        # Felix expects lower-case protocol strings.
                        rule['protocol'] = port['protocol'].lower()
                    except KeyError:
                        # Don't need the protocol.
                        pass
                    rules.append(rule)
                except KeyError:
                    # Skip this rule if it's missing a mandatory field.
                    # Per the Kube data model, containerPort is mandatory,
                    # protocol is not.
                    pass
        else:
            rules.append({

            })
        return rules

    def _apply_rules(self, profile_name, rules):
        """
        Generate a new profile with the specified rules.

        This contains inbound allow rules for all the ports we gathered,
        plus a default 'allow from <profile_name>' to allow traffic within a
        profile group.

        :param string profile_name: The profile to update
        :param list rules: The rules to set on the profile
        :return:
        """
        profile = {
            'id': profile_name,
            'inbound_rules': rules + [
                {
                    'action': 'allow',
                    'src_tag': profile_name
                },
                {
                    'action': 'deny',
                }
            ],
            'outbound_rules': [
                {
                    'action': 'allow',
                }
            ]
        }
        profile_string = json.dumps(profile, indent=2)
        print('Final profile "%s": %s' % (profile_name, profile_string))

        # Pipe the Profile JSON into the calicoctl command to update the rule.
        calicoctl('profile', profile_name, 'rule', 'update',
                  _in=profile_string)

        # Also add the workload to the profile.
        calicoctl('profile', profile_name, 'member', 'add', self.docker_id)

if __name__ == '__main__':
    print('Args: %s' % sys.argv)
    mode = sys.argv[1]

    if mode == 'init':
        print('No initialization work to perform')
    elif mode == 'setup':
        print('Executing Calico pod-creation plugin')
        NetworkPlugin().create(sys.argv)
    elif mode == 'teardown':
        print('No pod-deletion work to perform')
