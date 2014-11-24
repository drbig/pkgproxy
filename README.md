# pkgproxy

PkgProxy is a caching transparent HTTP proxy intended to save time and bandwidth spent on upgrading OS installations. It's written in Go.

Features:

- Minimal server and client configuration required
- No root or equivalent needed anywhere
- Simple but flexible
- Good performance from small to mid-sized client pools (i.e. not Google-scale yet)
- Works on all major modern OSes, no dependencies

## What it does

PkgProxy is a HTTP proxy designed specifically for caching packages as used by virtually all modern Unix-like systems. When you start it you can specify a file with Regexp-based filters for URIs that shouldn't be cached - e.g. the index or database files of your distro. Once the path has been successfully saved to disk next request for the same path will be served from the file (including partial content requests).

The general philosophy is that each request should be satisfied, so even if there is a local problem the request is passed upstream and the response is copied back to the client.

On the client side all you need to do is ensure your package manager is downloading packages via HTTP and uses the proxy you've just set up (on most Linuxes this boils down to setting up the `http_proxy` environment variable).

## Use cases

Say you use Vagrant daily for development and test deployments of your multi-tired app. You also have a Ansible playbook written for provisioning. Every time you spin up fresh VMs the whole process of checking for updates and downloading software gets repeated...

Get, build, setup and start PkgProxy:

    $ git clone https://github.com/drbig/pkgproxy.git
    Cloning into 'pkgproxy'...
    remote: Counting objects: 75, done.
    remote: Compressing objects: 100% (34/34), done.
    remote: Total 75 (delta 29), reused 75 (delta 29)
    Unpacking objects: 100% (75/75), done.
    Checking connectivity... done.
    $ cd pkgproxy
    $ go build
    $ mkdir packages
    $ ./pkgproxy -r packages/
    2014/11/24 17:27:15 Cache root at /home/drbig/pkgproxy/packages
    2014/11/24 17:27:15 No filters file given
    2014/11/24 17:27:15 Starting proxy server at :9999

Edit your playbook(s). At the top level:

    ---
    - hosts: all
      sudo: yes
      vars:
        pkg_proxy_env:
          http_proxy: http://127.0.0.1:9999/


And then for package-related tasks:

    tasks:
      - name: update system
        shell: apt-get update -qq && apt-get dist-upgrade -qq
        environment: pkg_proxy_env


And that's it. You'll cache everything, including the various Index files. Want to clean the cache? No problem:

    $ rm -rf packages/*

- - -

You have a local network of assorted OSes. Maybe you've setup a NFS share with `/var/cache/apt/archives` for your Debians, and/or a share with `/var/cache/pacman/pkg` for your ArchLinuxes...

Make a `filters.txt` to filter out the stuff you don't want to cache:

    $ cat <<EOF >/home/user/filters.txt
    ... /.*\.db
    ... /.*\.sig
    ... /.*\.gpg
    ... /Index.*
    ... /Translation-.*
    ... /Packages.*
    ... /Release.*
    ... /Sources.*
    ... /meta-release-.*
    ... EOF


Write a systemd unit file:

    $ cat <<EOF >/etc/systemd/system/pkgproxy.service
    ... [Unit]
    ... Description=Caching package proxy
    ... After=network.target
    ... 
    ... [Service]
    ... ExecStart=/home/user/pkgproxy/pkgproxy -a 192.168.0.1:9999 -f /home/user/filters.txt -r /mnt/data/pkgcache
    ... Restart=always
    ... 
    ... [Install]
    ... WantedBy=multi-user.target
    ... EOF


Setup, start and verify:

    $ mkdir /mnt/data/pkgcache
    $ systemctl enable pkgproxy
    Created symlink from /etc/systemd/system/multi-user.target.wants/pkgproxy.service to /etc/systemd/system/pkgproxy.service.
    $ systemctl start pkgproxy
    $ systemctl status pkgproxy
    ● pkgproxy.service - Caching package proxy
       Loaded: loaded (/etc/systemd/system/pkgproxy.service; enabled)
       Active: active (running) since Mon 2014-11-24 17:59:44 CET; 4s ago
     Main PID: 5689 (pkgproxy)
       CGroup: /system.slice/pkgproxy.service
               └─5689 /home/user/pkgproxy/pkgproxy -a 192.168.0.1:9999 -f /...
    
    Nov 24 17:59:44 rpi systemd[1]: Started Caching package proxy.
    Nov 24 17:59:45 rpi pkgproxy[5689]: 2014/11/24 17:59:45 Cache root at /...e
    Nov 24 17:59:45 rpi pkgproxy[5689]: 2014/11/24 17:59:45 Parsed 9 filter...t
    Nov 24 17:59:45 rpi pkgproxy[5689]: 2014/11/24 17:59:45 Starting proxy ...9
    Hint: Some lines were ellipsized, use -l to show in full.


Server setup's done. Check the documentation of your distros/OSes package management software on how to set them up to use a HTTP proxy. E.g. for Debians all that is needed is:

    $ echo Acquire::http::Proxy "http://192.168.0.1:9999"\; >> /etc/apt/apt.conf


## Additional notes

Currently the major scale-limiting factor is that path locking is done internally. If one were to replace code in `barrier_simple.go` with a MemCache- or Doozer-based lookup one could run multiple instances of PkgProxy all serving from a shared cache root. Under high-load but with multiple instances the overhead added by using an external service should be negligible.

There are also two very basic statistics accessible at `http://proxy.ip:port/debug/vars`: `statsCacheBytes` is the number of response body bytes served from the cache, and `statsDownBytes` is the number of response body bytes actually downloaded from upstream.

## Contributing

Follow the usual GitHub development model:

1. Clone the repository
2. Make your changes on a separate branch
3. Make sure you run `gofmt` and `go test` before committing
4. Make a pull request

See licensing for legalese.

## Licensing

Standard two-clause BSD license, see LICENSE.txt for details.

Any contributions will be licensed under the same conditions.

Copyright (c) 2014 Piotr S. Staszewski
