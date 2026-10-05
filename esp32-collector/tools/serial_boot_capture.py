#!/usr/bin/env python3
"""Reset an ESP32 and capture its boot log, surviving USB re-enumeration.

Why this exists: on boards whose console is the native USB-Serial/JTAG
peripheral (e.g. ESP32-C6), a reset *disconnects the CDC device*.  The port
node changes (/dev/ttyACM1 -> /dev/ttyACM0) and the by-id symlink is recreated,
so a plain "open once and read" script dies with:

    SerialException: device reports readiness to read but returned no data
    (device disconnected or multiple access on port?)

This script re-opens the port on every read failure and keeps collecting, so
one invocation captures the whole reset->boot->steady-state sequence.

Pass the by-id symlink, not /dev/ttyACMn: the symlink follows the board across
re-enumeration while the ttyACM number does not.

    python3 serial_boot_capture.py \
        /dev/serial/by-id/usb-Espressif_USB_JTAG_serial_debug_unit_<MAC>-if00 \
        40 /tmp/boot.log

Reset sequence follows docs/验证/验收矩阵-内存与吞吐-2026-10-05.md: assert RTS
(EN) briefly with DTR released.  Read-only with respect to flash.
"""
import sys
import time

import serial


def open_port(port, attempts=40):
    for _ in range(attempts):
        try:
            s = serial.Serial(port, 115200, timeout=0.2)
            time.sleep(0.25)
            return s
        except Exception:
            time.sleep(0.25)
    return None


def main():
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    port = sys.argv[1]
    dur = float(sys.argv[2]) if len(sys.argv) > 2 else 20.0
    out = sys.argv[3] if len(sys.argv) > 3 else None

    s = open_port(port)
    if s is None:
        print("CANNOT OPEN", port)
        return 2

    # EN low then high; DTR released so GPIO0 stays high (normal boot, not download mode).
    s.setDTR(False)
    s.setRTS(True)
    time.sleep(0.15)
    s.setRTS(False)

    buf = bytearray()
    t0 = time.time()
    while time.time() - t0 < dur:
        try:
            d = s.read(8192)
            if d:
                buf += d
        except Exception:
            try:
                s.close()
            except Exception:
                pass
            s = open_port(port)
            if s is None:
                break
    try:
        s.close()
    except Exception:
        pass

    txt = buf.decode("utf-8", "replace")
    sys.stdout.write(txt)
    if out:
        with open(out, "w") as f:
            f.write(txt)
    return 0


if __name__ == "__main__":
    sys.exit(main())
