# nextnet

`nextnet` is a small Go daemon that presents an ESP8266-style AT interface to
software running in the ZX Spectrum Next MiSTer core. It uses the core's UART0
path and MiSTer's Linux networking stack.

The UART path and single-connection TCP stage are hardware-proven. The
repository now also contains the host-tested multiplexed TCP stage:

- exact active-core detection through `/tmp/CORENAME`
- raw Linux serial setup (115200 8N1 by default)
- activation only when the trimmed core name is exactly `ZXNext`
- immediate UART shutdown for every other core name, including alternative
  launcher values
- serialized, bounded UART output
- ESP basic and fixed-115200 UART compatibility commands
- virtual reset messages reporting `WIFI CONNECTED` and `WIFI GOT IP`
- single-connection TCP through `CIPMUX=0`, `CIPSTART`, `CIPSEND`, and
  `CIPCLOSE`
- up to five simultaneous TCP connections through `CIPMUX=1` and link IDs
  `0` through `4`
- fixed-length, binary-safe `CIPSEND` payload handling up to 64 KiB
- mux-aware `CONNECT`, `+IPD`, and `CLOSED` notifications
- `CIPSTATUS` reporting for active connections
- bounded socket-to-UART backpressure and clean socket shutdown
- default console logging for every command, response, malformed line, and
  unsupported command
- credential redaction for `AT+CWJAP*` commands

UDP, inbound TCP servers, and real Wi-Fi configuration are not implemented.

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

These applications prove the `CIPMUX=0` path on hardware. Multiplexed sockets
remain host-tested until a suitable real Next application or UART fixture is
run against them.

The serial device remains configurable because this one-system result is not a
guarantee for every MiSTer installation.

## Build and test

Requirements: Go 1.22 or later.

```sh
make test
make build
make build-mister
```

The MiSTer build is a stripped, CGO-free Linux ARMv7 executable at:

```text
dist/nextnet-linux-armv7
```

## Run on MiSTer

Copy the ARMv7 executable to MiSTer, make it executable, and run it as root:

```sh
/media/fat/nextnet/nextnet -device /dev/ttyS1 -baud 115200
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
Credential-bearing `AT+CWJAP*=` commands are logged as `<redacted>`. Future
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
AT+UART_CUR=115200,8,1,0,0
AT+UART_DEF=115200,8,1,0,0
AT+UART=115200,8,1,0,0

AT+CIPMUX?
AT+CIPMUX=0
AT+CIPMUX=1
AT+CIPSTATUS

AT+CIPSTART="TCP","host",port
AT+CIPSEND=length
AT+CIPCLOSE

AT+CIPSTART=id,"TCP","host",port
AT+CIPSEND=id,length
AT+CIPCLOSE=id
```

UART setters are accepted only when they match the daemon's configured fixed
baud and raw 8N1/no-flow-control settings. This avoids claiming a baud change
that Linux termios has not actually performed.

## MiSTer acceptance tests

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
`+IPD,id,length:<payload>`. Leaving ZXNext must close both sockets and return
the daemon to its idle state.
