# nextnet

`nextnet` is a small Go daemon that presents an ESP8266-style AT interface to
software running in the ZX Spectrum Next MiSTer core. It uses the core's UART0
path and MiSTer's Linux networking stack.

This repository currently implements the first hardware-proven stage:

- exact active-core detection through `/tmp/CORENAME`
- raw Linux serial setup (115200 8N1 by default)
- activation only when the trimmed core name is exactly `ZXNext`
- immediate UART shutdown for every other core name, including alternative
  launcher values
- serialized, bounded UART output
- `AT`, `ATE0`, `ATE1`, `AT+GMR`, and `AT+RST`
- default console logging for every command, response, malformed line, and
  unsupported command
- credential redaction for `AT+CWJAP*` commands

TCP/UDP socket commands are not part of this stage.

## Verified MiSTer hardware

Testing on a real MiSTer with the ZXNext core established that:

- `/tmp/CORENAME` contains `ZXNext` while the core is active.
- `/dev/ttyS1` is the accessible core UART on the tested system.
- no existing process owned `/dev/ttyS1` during the test.
- raw 115200 baud decoded `\r\nAT\r\n` correctly.
- ZXNext's `.uart` command exchanged data in both directions with Linux.

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
`CIPSEND` payloads will be logged by length rather than content.

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

## First acceptance test

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
