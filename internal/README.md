# nextnet

`nextnet` is a small Go daemon that presents an ESP8266-style AT interface to
software running in the ZX Spectrum Next MiSTer core. It uses the core's UART0
path and MiSTer's Linux networking stack.

The UART path and fixed-length single-connection TCP stage are hardware-proven.
The repository now also contains host-tested multiplexed, transparent, and
inbound TCP stages:

- exact active-core detection through `/tmp/CORENAME`
- raw Linux serial setup (115200 8N1 by default)
- activation only when the trimmed core name is exactly `ZXNext`
- immediate UART shutdown for every other core name, including alternative
  launcher values
- serialized, bounded UART output
- ESP basic commands and live UART baud changes, including non-standard Next
  rates such as 230769 baud
- virtual reset messages reporting `WIFI CONNECTED` and `WIFI GOT IP`
- single-connection TCP through `CIPMUX=0`, `CIPSTART`, `CIPSEND`, and
  `CIPCLOSE`
- up to five simultaneous TCP connections through `CIPMUX=1` and link IDs
  `0` through `4`
- one inbound TCP listener through `CIPSERVER`, with accepted clients assigned
  the lowest available multiplexed link ID
- fixed-length, binary-safe `CIPSEND` payload handling up to 64 KiB
- single-connection transparent TCP through `CIPMODE=1` and bare `CIPSEND`
- raw bidirectional UART/TCP forwarding with `+++` escape handling
- hardware ESP reset detection through CTS when used with the patched ZXNext
  core, including socket teardown and a clean emulator restart
- optional `CIPSTART` TCP keepalive intervals
- virtual station-mode Wi-Fi compatibility through `CWMODE`, `CWLAP`, and
  `CWJAP`, with MiSTer Linux IP and MAC reporting through `CIFSR`
- active and passive socket receive modes through `CIPRECVMODE`,
  `CIPRECVDATA`, and `CIPRECVLEN`
- mux-aware `CONNECT`, `+IPD`, and `CLOSED` notifications
- `CIPSTATUS` reporting for active connections
- bounded socket-to-UART backpressure and clean socket shutdown
- default console logging for every command, response, malformed line, and
  unsupported command
- credential redaction for `AT+CWJAP*` commands

UDP, SSL, and real Wi-Fi configuration are not implemented.

## Verified MiSTer hardware

Testing on a real MiSTer with the ZXNext core established that:

- `/tmp/CORENAME` contains `ZXNext` while the core is active.
- `/dev/ttyS1` is the accessible core UART on the tested system.
- no existing process owned `/dev/ttyS1` during the test.
- raw 115200 baud decoded `\r\nAT\r\n` correctly.
- ZXNext's `.uart` command exchanged data in both directions with Linux.
- ZXDB-dl successfully searched the database and downloaded a game through
  `nextnet`.
- GETIT also completed network downloads successfully.

These applications prove the `CIPMUX=0` path on hardware. Multiplexed sockets,
inbound servers, and passive reception remain host-tested until a suitable real
Next application or UART fixture is run against them.

The serial device remains configurable because this one-system result is not a
guarantee for every MiSTer installation.

## Build and test

Requirements: Go 1.22 or later. Building ZIP releases additionally requires
`zip`, `sha256sum`, and GNU `touch`.

FPGA changes use a separate Quartus toolchain and ZXNext core checkout. See
[Building and deploying the ZX Spectrum Next MiSTer core](../ZXNEXT_MISTER_CORE_BUILD.md).

```sh
make test
make version
make build
make build-mister
make package-mister
make release-zip
```

`make version` shows the value baked into builds made from the current checkout:

- a clean commit with an exact tag uses that tag
- a clean untagged commit uses `git rev-parse --short HEAD`
- any staged, unstaged, or untracked change uses the short hash followed by
  `[dirty]`

For example, `v0.1.0`, `a1b2c3d`, and `a1b2c3d[dirty]` are possible program
versions. The same value is printed by `nextnet -version`, logged at startup,
and returned in the `AT+GMR` response. Builds made directly with `go build`
instead of the project build targets retain the `dev` fallback.

The MiSTer build is a stripped, CGO-free Linux ARMv7 executable at:

```text
dist/nextnet-linux-armv7
```

`make package-mister` creates an installable archive and checksum:

```text
dist/nextnet-mister-armv7.tar.gz
dist/nextnet-mister-armv7.tar.gz.sha256
```

For a distributable ZIP containing the binary, installer, uninstaller, README,
and BASIC reset example, run:

```sh
./scripts/build-release.sh
```

The ZIP filename uses the detected version. Characters unsuitable for an
archive name are replaced with `-`, so a dirty program version such as
`a1b2c3d[dirty]` produces:

```text
dist/nextnet-mister-armv7-a1b2c3d-dirty.zip
dist/nextnet-mister-armv7-a1b2c3d-dirty.zip.sha256
```

## Create a tagged release

After committing all release changes, supply the new version to:

```sh
./scripts/release.sh v1.2.3
```

The script requires a named branch and a completely clean working tree,
including no staged or untracked files. It rejects an invalid or existing tag,
runs the test suite, creates an annotated tag on the current commit, and builds
fresh host, ARMv7 tar, and versioned ZIP outputs under `dist/`. It verifies that
the tag is the version detected by the build, then pushes only that tag.

The configured remote for the current branch is used when available. Otherwise
the script uses `origin`, or the sole configured remote. The tag is pushed only
after all builds succeed. If building or pushing fails, the newly-created local
tag is removed so the release can be corrected and retried.

## Install on MiSTer

Copy either package to a permanent location on the MiSTer SD card, extract it,
and run the installer as root. For the ZIP release:

```sh
sha256sum -c nextnet-mister-armv7-v0.1.0.zip.sha256
unzip nextnet-mister-armv7-v0.1.0.zip
cd nextnet-mister
./install.sh
```

For the tar archive:

```sh
sha256sum -c nextnet-mister-armv7.tar.gz.sha256
tar -xzf nextnet-mister-armv7.tar.gz
cd nextnet-mister
./install.sh
```

The installer can safely be run again after replacing or updating the bundle.
It:

- makes the existing `nextnet` binary executable and runs it from the directory
  where the package was extracted; it does not copy the program elsewhere
- preserves unrelated contents of `/media/fat/linux/user-startup.sh`
- adds exactly one marked automatic-startup block using the binary's absolute
  path
- saves the original startup file as `user-startup.sh.nextnet.bak` the first
  time it edits an existing file
- starts the daemon immediately unless it is already running

For example, if the archive was extracted directly under `/media/fat`, then at
subsequent MiSTer boots `user-startup.sh` launches:

```sh
/media/fat/nextnet-mister/nextnet -device /dev/ttyS1 -baud 115200
```

Do not move or delete the extracted directory after installation. If it is
moved, run `install.sh` again from the new location to update the startup path.

Output is redirected to the volatile `/tmp/nextnet.log`, avoiding continuous
writes to the SD card. The daemon holds `/tmp/nextnet.lock`, so manual or
duplicate startup attempts cannot own the UART simultaneously.

To stop nextnet and remove only its marked startup block, run the uninstaller
from that same extracted directory:

```sh
./uninstall.sh
```

The uninstaller leaves the package files in place so they can be installed
again or deleted manually.

## Run manually on MiSTer

For development, copy the ARMv7 executable to MiSTer, make it executable, and
run it as root from wherever it was copied:

```sh
/media/fat/nextnet -device /dev/ttyS1 -baud 115200
```

The `-device` option can be omitted to auto-select `/dev/ttyS1` when it exists.
Keeping it explicit during early hardware testing makes the selected device
unambiguous.

The daemon remains idle until `/tmp/CORENAME`, after whitespace trimming, is
exactly `ZXNext`. Any other value—or a missing, unreadable, or empty core
file—closes the serial transport and resets the emulator session.

Use Ctrl-C or send `SIGTERM` for a clean shutdown.

## Command logging

Logs are written to standard error. AT command logging is always enabled at the
default `info` level:

```text
time=... level=INFO msg="AT <- AT"
time=... level=INFO msg="AT supported" command=AT
time=... level=INFO msg="AT -> OK"
time=... level=WARN msg="AT unsupported" command=AT+UNKNOWN
time=... level=INFO msg="AT -> ERROR"
```

`ATE0` disables UART command echo but does not disable console logging.
Credential-bearing `AT+CWJAP*=` commands are logged as `<redacted>`. Supplied
Wi-Fi credentials are never written to logs. `CIPSEND` payloads and incoming
network data are logged by length rather than content.

Example TCP logs:

```text
time=... level=INFO msg="AT <- AT+CIPSTART=\"TCP\",\"example.com\",80"
time=... level=INFO msg="socket connected" id=0 network=tcp host=example.com port=80
time=... level=INFO msg="AT <- AT+CIPSEND=18"
time=... level=INFO msg="CIPSEND payload received" id=0 bytes=18
time=... level=INFO msg="socket sent" id=0 bytes=18
time=... level=INFO msg="socket received" id=0 bytes=512
time=... level=INFO msg="transparent mode entered" id=0
time=... level=INFO msg="transparent socket sent" id=0 bytes=24
time=... level=INFO msg="transparent mode exited" cause="escape sequence"
time=... level=INFO msg="ESP hardware reset detected" device=/dev/ttyS1 signal=CTS
time=... level=INFO msg="ESP emulator reset" device=/dev/ttyS1
```

Available options:

```text
-device string
      serial device (default: auto-detect /dev/ttyS1)
-baud int
      UART baud rate (default 115200)
-core string
      exact MiSTer core name that activates the bridge (default "ZXNext")
-core-file string
      file containing the active MiSTer core name (default "/tmp/CORENAME")
-log-level string
      debug, info, warn, or error (default "info")
-version
      print version and exit
```

## Supported AT subset

```text
AT
ATE0
ATE1
AT+GMR
AT+RST

AT+UART_CUR?
AT+UART_DEF?
AT+UART?
AT+UART_CUR=baud,8,1,0,0
AT+UART_DEF=115200,8,1,0,0
AT+UART=baud,8,1,0,0

AT+CWMODE?
AT+CWMODE_CUR?
AT+CWMODE_DEF?
AT+CWMODE=1
AT+CWMODE_CUR=1
AT+CWMODE_DEF=1
AT+CWLAP
AT+CWJAP
AT+CWJAP?
AT+CWJAP_CUR?
AT+CWJAP_DEF?
AT+CWJAP="ssid","password"
AT+CWJAP_CUR="ssid","password"
AT+CWJAP_DEF="ssid","password"
AT+CIFSR

AT+CIPMUX?
AT+CIPMUX=0
AT+CIPMUX=1
AT+CIPSTATUS

AT+CIPDINFO?
AT+CIPDINFO=0
AT+CIPDINFO=1
AT+CIPRECVMODE?
AT+CIPRECVMODE=0
AT+CIPRECVMODE=1
AT+CIPRECVLEN?
AT+CIPRECVDATA=length
AT+CIPRECVDATA=id,length
AT+CIPMODE?
AT+CIPMODE=0
AT+CIPMODE=1

AT+CIPSERVER?
AT+CIPSERVER=0
AT+CIPSERVER=0,close_connections
AT+CIPSERVER=1
AT+CIPSERVER=1,port
AT+CIPSERVER=1,port,"TCP"
AT+CIPSTO?
AT+CIPSTO=timeout_seconds

AT+CIPSTART="TCP","host",port
AT+CIPSTART="TCP","host",port,keepalive_seconds
AT+CIPSEND=length
AT+CIPSEND
AT+CIPCLOSE

AT+CIPSTART=id,"TCP","host",port
AT+CIPSTART=id,"TCP","host",port,keepalive_seconds
AT+CIPSEND=id,length
AT+CIPCLOSE=id
AT+CIPCLOSE=5
```

With `CIPMUX=0`, `CIPMODE=1` enables transparent receiving for an active TCP
connection, so TCP bytes reach UART without AT framing. Bare `AT+CIPSEND`
returns a prompt and also enables transparent UART-to-TCP transmission.
Sending exactly `+++` with the ESP guard intervals (more than 20 ms before and
after it, and less than 20 ms between pluses) exits transparent transmission
without forwarding those three bytes. AT commands can then be used again while
TCP-to-UART reception remains transparent.

`CIPRECVMODE=0` sends socket payloads immediately in unsolicited `+IPD`
frames. `CIPRECVMODE=1` retains TCP data in a bounded buffer and reports the
available length; applications retrieve binary-safe chunks with
`CIPRECVDATA`, while `CIPRECVLEN?` reports all five buffered lengths. A remote
`CLOSED` notification is deferred until its buffered data has been read.
`CIPDINFO=1` adds the remote address and port to active-mode `+IPD` headers and
passive `CIPRECVDATA` responses. `CIPMODE=1` continues to use transparent raw
reception instead of passive buffering.

`AT+CIPSERVER=1,port` listens on all MiSTer network interfaces and requires
`CIPMUX=1`. One TCP server can run at a time. Incoming clients take the lowest
free ID from `0` through `4`, emit `id,CONNECT`, and then use the same
`CIPSEND`, `CIPCLOSE`, `CIPSTATUS`, and receive paths as outgoing sockets.
`CIPSERVER=0` stops accepting clients but preserves existing connections;
`CIPSERVER=0,1` also closes connections. `CIPCLOSE=5` closes every connection
without stopping the listener. `CIPSTO` sets an inactivity timeout from 0 to
7200 seconds for accepted clients; incoming client traffic restarts the timer,
while data sent by the server does not. A value of `0` disables the timeout.
The listener and all clients are always closed on ESP reset, core exit, or
daemon shutdown.

`AT+UART_CUR` and legacy `AT+UART` accept rates from 80 through 5000000 baud
with raw 8N1/no-flow-control framing. nextnet sends and drains `OK` at the old
rate before changing Linux termios, and supports non-standard values such as
230769 through `termios2`. `AT+RST` restores the startup baud. Persistent
`AT+UART_DEF` changes are not implemented; its setter is accepted only for the
configured startup value.

`CWMODE`, `CWMODE_CUR`, and `CWMODE_DEF` expose station mode (`1`), because
the MiSTer host supplies the network connection and nextnet does not emulate a
real Wi-Fi radio. `AT+CWLAP` reports one virtual open access point named
`MiSTer`, using the host interface MAC address. `CWJAP` query and join variants
report or update the virtual SSID and emulate a successful connection without
reconfiguring Linux or retaining the supplied password. `AT+CIFSR` reports an
active non-loopback IPv4 address and MAC address from MiSTer Linux. If no
suitable interface can be discovered, it returns `0.0.0.0` and
`00:00:00:00:00:00` while keeping the AT session alive.

## MiSTer acceptance tests

### ESP reset test

[`examples/esp-reset.bas`](../examples/esp-reset.bas) is a short NextBASIC source
listing that pulses NextReg `$02` bit 7 for ten video frames:

```basic
10 REM NEXTNET ESP RESET TEST
20 CLS
30 PRINT "RESETTING ESP..."
40 REG 2,128
50 PAUSE 10
60 REG 2,0
70 PRINT "ESP RESET RELEASED"
```

Enter the listing in NextBASIC and run it while nextnet is active. With the
patched core, `/tmp/nextnet.log` should contain `ESP hardware reset detected`
followed by `ESP emulator reset`. Always execute `REG 2,0` to release the reset
line; if the program is interrupted between lines 40 and 60, enter that command
manually at the BASIC prompt.

With the daemon running and ZXNext active, send:

```text
AT\r\n
```

The emulator returns an optional command echo followed by:

```text
\r\nOK\r\n
```

Switch away from ZXNext and verify the log reports:

```text
serial closed; bridge idle
```

ZXDB-dl and GETIT are the current `CIPMUX=0` regression applications. A mux
acceptance run should open two IDs, send data independently with
`AT+CIPSEND=id,length`, and verify incoming frames use
`+IPD,id,length:<payload>`. An inbound acceptance run should create a server,
connect to the MiSTer port from another machine, retrieve passive data with
`CIPRECVDATA`, and send a reply through that connection ID. Leaving ZXNext must
close the listener and every socket, returning the daemon to its idle state.
The observed NXTEL transparent-mode command sequence is covered by host tests.
With the patched core installed, NXTEL's ESP reset should log `ESP hardware
reset detected`, close its transparent socket, and restart the emulator in AT
command mode. That complete reset sequence remains to be verified on MiSTer.
