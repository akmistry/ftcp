# ftcp: A user-space, failover, high availability TCP implementation

ftcp is a user-space TCP written in Go, intended to experiment with the idea of
a failover-capable stack for high-reliability/availability applications.

ftcp is NOT:
- A production-quality stack
- Designed for high performance
- Spec compliant

There are intentionally non-goals. Rather, the focus is on a straight-forward,
easy to understand implementation, which can be used to experiment with some
ideas. In particular, synchronous replication of the TCP connection state, to
allow failover to a standby when the original endpoint fails (i.e. software
crash, hardware failure, someone tripped over the ethernet cable... ooops).

The motivation behind this was dealing with some legacy protocols. These
protocols lack retry or session resumption abilities, and tie session state to
the TCP connection. If the server end of the connection fails, session recovery
may be very difficult or costly (i.e. retrying an FTP upload of a 100G file 99G
into the upload. Eek!).

A quick web search reveals some acedemic research into this. In terms of
existing software,
[tcpcp](https://www.kernel.org/doc/ols/2004/ols2004v1-pages-9-22.pdf) is
probably the most well known. Like ftcp, tcpcp requires two servers to be
in collusion. Whereas tcpcp is (quoted from the paper):
>  ...primarily designed for scenarios, where the old and the new connection
>  owner are both functional during the process of connection passing.

ftcp is specifically intended for the situation where the old server is
non-functional.

## Running the code

The code is a hack! Sorry. But if you're so inclined to run it, a simple test
server is provided in `cmd/test`. This runs an echo server on port 9999 (hard
coded into the TCP stack). The server's IP address is hard coded and will need
to be changed to your server's IP address.

Before running the test server, you'll need to tell iptables to DROP all
packets inbound to TCP port 9999, to prevent the kernel's TCP stack from
responding to new connections requests (with a RST response, because there is
no server running).
```
sudo iptables -A INPUT -d 192.168.1.134 -p tcp --dport 9999 -j DROP
```
Replace `192.168.1.134` with your sever's IP address.

The test server needs to run as root, due to the use of raw sockets. Once running,
you can use `nc` from another device to echo a file:
```
nc -v 192.168.1.134 9999 < random.test > random.received
```
If the transfer succeeded, the contents of `random.received` should be idential
to `random.test`

## License

This project is provided under a 3-clause BSD license.
