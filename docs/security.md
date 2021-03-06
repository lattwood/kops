## Security Notes for Kubernetes

## SSH Access

SSH is allowed to the masters and the nodes, by default from anywhere.

To change the CIDR allowed to access SSH (and HTTPS), set AdminAccess on the cluster spec.

When using the default images, the SSH username will be `admin`, and the SSH private key is be
the private key corresponding to the public key in `kops get secrets --type sshpublickey admin`.  When
creating a new cluster, the SSH public key can be specified with the `--ssh-public-key` option, and it
defaults to `~/.ssh/id_rsa.pub`.

To change the SSH public key on an existing cluster:

* `kops delete secret --name <clustername> sshpublickey admin`
* `kops create secret --name <clustername> sshpublickey admin -i ~/.ssh/newkey.pub`
* `kops update cluster --yes` to reconfigure the auto-scaling groups
* `kops rolling-update --yes` to immediately roll all the machines so they have the new key (optional)


## Kubernetes API

(this section is a work in progress)

Kubernetes has a number of authentication mechanisms:

### API Bearer Token

The API bearer token is a secret named 'admin'.

`kops get secrets admin -oplaintext` will show it

### Admin Access

Access to the administrative API is stored in a secret named 'kube':

`kops get secrets kube -oplaintext` or `kubectl config view --minify` to reveal
