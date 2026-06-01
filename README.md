# `lysekrone`: LD_PRELOAD client library for xsocket.

`macambra` is a SOCKS5 to SOCKS5 port forwarder with full `UDP Associate`, `BIND` and `Tor` support, as well as [xsocket](https://github.com/koro666/xsocket) support, meaning you can forward any SOCKS5 port to a network namespace or VRF transparently. It is intended mainly for forwarding `Tor` SOCKS5 port to a network namespace allowing to securely use the proxy and connecting to a VPN over Tor inside a network namespace.

## Build

This project has no external dependencies, just clone the repository and compile with `Go` as usual.

```
git clone https://github.com/schropkev/macambra
cd macambra
go build
```

If you wish to compile this program into a static binary:

`CGO_ENABLED=0 go build`

## Usage

Its usage is:

`./macambra -l <listen_addr_port> -f <upstream_socks5_address_port> [ -uds-listen @xsocket-socket|path/to/xsocket_socket -uds-connect @xsocket-socket|path/to/xsocket_socket ]`

`-l` and `-f` are required and these can use IPv4 or `[bracketed]` IPv6 adresses, `-uds-listen` and `-uds-connect` are optional . `-uds-listen` and `-uds-connect` can accept `xsocket-server` Unix sockets as a file path or abstract ones.

**Example #1:**

`./macambra -l 127.0.0.1:1080 -f 127.0.0.1:9150`

Simple forwarding for showing an example, useless without `-uds-listen` and `-uds-connect` (I mean useless because it will the same thing as you connect directly to the upstream SOCKS5 port).

**Example #2:**

`./macambra -l [::1]:1080 -f 127.0.0.1:9150 -uds-listen /var/xsocket_socket`

Send SOCKS5 requests to `127.0.0.1:9150` in the host namespace (in which `macambra` is running) and listen in the network namespace in which `xsocket-server` is running with Unix socket `"/var/xsocket_socket"` active. Default scheme for forwarding `Tor` SOCKS5 port to a network namespace.

**Example #3:**

`./macambra -l 127.0.0.1:1080 -f 127.0.0.1:9150 -uds-listen @xsocket-socket -uds-connect /var/xsocket_socket`

Listen in the VRF in which `xsocket-server` is running with the Unix socket `"@xsocket-socket"` ( `-uds-listen` ) and forward to the network namespace in which another `xsocket-server` is running with `"/var/xsocket_socket"` ( `-uds-connect` ). The default usage with VRFs is to use a abstract Unix socket as it is in this example, but if the VRF interface is inside a isolated network namespace outside the host side, an Unix socket as a file path is needed (abstract Unix sockets don't bypass a network namespace boundary).

**Notes:**

You can use either `-uds-listen` and `-uds-connect` or both at same time.

### Connecting to a VPN over Tor inside a network namespace is easy with Macambra:

You just need to install [xsocket](https://github.com/koro666/xsocket), compile `Macambra` and install `OpenVPN` as well as having a `OpenVPN` configuration file with TCP support.

Compile and install [xsocket](https://github.com/koro666/xsocket):

```
git clone https://github.com/koro666/xsocket
cd xsocket
meson setup build
meson compile -C build
sudo meson install -C build
```

Add network namespaces:

```
sudo ip netns add somenetns
sudo ip netns exec somenetns ip link set lo up
```

Run [xsocket](https://github.com/koro666/xsocket) inside that network namespace:

`sudo ip netns exec somenetns xsocket-server /tmp/xs-socket`

In another terminal tab, run `Macambra` with listening point pointed to the network namespace (assuming you have `Tor/TorBrowser` installed and running):

`./macambra -l 127.0.0.1:1080 -f 127.0.0.1:9050 -uds-listen /tmp/xs-socket`

Now you should add fake routes inside that network namespace for `OpenVPN` has no problems when completing its connection:

```
sudo ip netns exec somenetns ip -4 route add default dev lo
sudo ip netns exec somenetns ip -6 route add default dev lo
```

Now you should have an `OpenVPN` configuration file with TCP support by default, if you don't, pick up some `OpenVPN` files in [VPN Gate](https://www.vpngate.net/en/) (you will need to test some `VPN Gate` files until you find one that is working).
Once you have the TCP `OpenVPN` configuration file, do this:

`sudo ip netns exec somenetns openvpn --socks-proxy 127.0.0.1 1080 --config /path/to/openvpn/config_file.ovpn`

If you downloaded files from [VPN Gate](https://www.vpngate.net/en/), run this instead of above one:

`sudo ip netns exec somenetns openvpn --socks-proxy 127.0.0.1 1080 --data-ciphers AES-128-CBC --config /path/to/openvpn/config_file.ovpn`

Wait some seconds and check there is a `tun0` interface inside the network namespace:

`sudo ip netns exec somenetns ip link show ## you will need to do this some times if using VPN Gate configuration files`

Add a DNS server in a `resolv.conf` for making possible to browse sites and domains:

```
sudo mkdir -p /etc/netns/somenetns
sudo echo "nameserver 9.9.9.9" > /etc/netns/somenetns/resolv.conf
```

Now, just run a unprivileged shell with sound and D-Bus support (assuming you running at you graphical Xorg session) and enjoy a very private Internet access:

`sudo ip netns exec nsx sudo DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$(id -u $(ls -l ${XAUTHORITY} | awk '{ print $3 }'))/bus PULSE_SERVER=/run/user/$(id -u $(ls -l ${XAUTHORITY} | awk '{ print $3 }'))/pulse/native PULSE_COOKIE=/home/$(ls -l ${XAUTHORITY} | awk '{ print $3 }')/.config/pulse/cookie -u someuser bash`

Replace `someuser` in `"-u someuser"` with your default user.

If you are in text mode graphics or don't want to run the shell as the Xorg user, jus run

`sudo ip netns exec nsx sudo -u someuser bash`

--------------------

## Thanks

- [@koro666](https://github.com/koro666) for his awesome project [xsocket](https://github.com/koro666/xsocket).

--------------------

Created on May 31, 2026.
