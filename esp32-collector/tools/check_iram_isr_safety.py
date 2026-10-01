#!/usr/bin/env python3
"""Post-link assertion: no IRAM function may reach flash code from the Wi-Fi ISR.

Why this exists
---------------
The Wi-Fi interrupt is registered *raw* via `xt_set_interrupt_handler`
(esp_wifi/<target>/esp_adapter.c `set_isr_wrapper`), so it never goes through
`esp_intr_alloc()`.  `esp_intr_noniram_disable()` only masks interrupts recorded
in `non_iram_int_mask[]`, which `esp_intr_alloc()` populates.  A raw ISR is
therefore NOT masked when the flash cache is disabled -- and the cache really is
disabled during OTA flash writes (`CONFIG_SPI_FLASH_AUTO_SUSPEND` is off).

Consequence: any byte the Wi-Fi ISR can reach must live in IRAM.  If a function
in the ISR's call graph is flash-resident, it crashes with an IllegalInstruction
precisely while an OTA is writing flash -- i.e. exactly when the device is being
repaired in the field.

That property is invisible to the linker and to IDF's own runtime check
(`esp_intr_ptr_in_isr_region` only sees `esp_intr_alloc`'d handlers).  It used to
be held by a one-off manual disassembly scan; this script makes it a gate that
re-runs on every build, so an IDF bump or a linker-fragment change cannot
silently move an ISR-reachable callee into flash.

What it checks
--------------
1. Direct and indirect (literal-pool `l32r` + `callxN`) control transfers.
2. Reachability from each seeded ISR entry point, walking only IRAM sources.
3. Reports any flash target reachable from a seed.

Usage
-----
    check_iram_isr_safety.py <elf> [--target esp32s3|esp32c6]

Exit status 0 = safe, 1 = unsafe, 2 = could not analyse (treated as failure by
callers, because "cannot verify" must not pass silently).
"""

from __future__ import annotations

import argparse
import os
import re
import shutil
import subprocess
import sys

# Wi-Fi ISR entry points that are registered raw and must stay IRAM-only.
# Names come from the closed-source Wi-Fi library; keep the list explicit so a
# rename shows up as "seed not found" rather than silently checking nothing.
ISR_SEEDS = ("wDev_ProcessFiq",)

TOOLCHAINS = {
    "esp32s3": ("xtensa-esp-elf", "xtensa-esp-elf-objdump"),
    "esp32c6": ("riscv32-esp-elf", "riscv32-esp-elf-objdump"),
}


def find_objdump(target: str) -> str | None:
    """Locate the matching objdump under the Espressif tools tree."""
    prefix, tool = TOOLCHAINS[target]
    root = os.path.expanduser("~/.espressif/tools")
    if os.path.isdir(root):
        for name in sorted(os.listdir(root)):
            if name.startswith(prefix):
                for ver in sorted(os.listdir(os.path.join(root, name)), reverse=True):
                    cand = os.path.join(root, name, ver, prefix, "bin", tool)
                    if os.path.isfile(cand):
                        return cand
    return shutil.which(tool)


def detect_target(elf: str, override: str | None) -> str:
    if override:
        return override
    with open(elf, "rb") as fh:
        head = fh.read(64)
    # Xtensa and RISC-V have different e_machine values.
    e_machine = int.from_bytes(head[18:20], "little")
    return {94: "esp32s3", 243: "esp32c6"}.get(e_machine, "esp32s3")


FUNC_RE = re.compile(r"^([0-9a-f]{8}) <([^>]+)>:")
LINE_RE = re.compile(r"^\s*([0-9a-f]+):\s+([0-9a-f]+)\s+(\S+)\s*(.*)$")
DIRECT_JUMPS = ("call0", "call4", "call8", "call12", "jx", "j")


class Regions:
    """Address ranges for IRAM and flash code, derived from the ELF symbols."""

    def __init__(self, symbols: list[tuple[int, int, str, str]]):
        self.iram: list[tuple[int, int, str]] = []
        self.flash: list[tuple[int, int, str]] = []
        for addr, size, name, sect in symbols:
            if not sect:
                continue
            if sect.startswith(".iram"):
                self.iram.append((addr, addr + max(size, 1), name))
            elif sect.startswith(".flash.text"):
                self.flash.append((addr, addr + max(size, 1), name))
        self.iram.sort()
        self.flash.sort()

    @staticmethod
    def _lookup(table, addr: int) -> str | None:
        lo, hi = 0, len(table) - 1
        while lo <= hi:
            mid = (lo + hi) // 2
            start, end, name = table[mid]
            if addr < start:
                hi = mid - 1
            elif addr >= end:
                lo = mid + 1
            else:
                return name
        return None

    def in_iram(self, addr: int) -> str | None:
        return self._lookup(self.iram, addr)

    def in_flash(self, addr: int) -> str | None:
        return self._lookup(self.flash, addr)


def read_symbols(objdump: str, elf: str) -> list[tuple[int, int, str, str]]:
    """Read (addr, size, name, section) from `objdump -t`.

    `nm` only reports a single-letter section code (T/t for text), which cannot
    distinguish .iram0.text from .flash.text, so use objdump's symbol table.
    """
    out = subprocess.run([objdump, "-t", elf], capture_output=True, text=True).stdout
    syms = []
    for line in out.splitlines():
        # "4037aa58 g     F .iram0.text\t0000009e wDev_ProcessFiq"
        parts = line.split()
        if len(parts) < 5:
            continue
        try:
            addr = int(parts[0], 16)
        except ValueError:
            continue
        sect = parts[3]
        name = parts[-1]
        try:
            size = int(parts[4], 16)
        except (ValueError, IndexError):
            size = 0
        syms.append((addr, size, name, sect))
    return syms


def analyse(elf: str, target: str):
    objdump = find_objdump(target)
    if not objdump:
        return None, f"objdump for {target} not found"
    syms = read_symbols(objdump, elf)
    regions = Regions(syms)
    if not regions.iram:
        return None, "no .iram* symbols found (wrong ELF or toolchain?)"

    out = subprocess.run([objdump, "-d", elf], capture_output=True, text=True).stdout

    calls: dict[str, set[tuple[str, object]]] = {}
    cur: tuple[int, str] | None = None
    pend: dict[str, tuple[str, object]] = {}

    for line in out.splitlines():
        m = FUNC_RE.match(line)
        if m:
            cur = (int(m.group(1), 16), m.group(2))
            calls.setdefault(cur[1], set())
            pend = {}
            continue
        if cur is None:
            continue
        m = LINE_RE.match(line)
        if not m:
            continue
        addr = int(m.group(1), 16)
        if regions.in_iram(addr) is None:
            continue  # only IRAM sources matter
        ins, rest = m.group(3), m.group(4)

        if ins == "l32r":
            reg = re.match(r"\s*(a\d+)\s*,", rest)
            tgt = re.search(r"\(([0-9a-f]{8})\s*<([^>]+)>", rest)
            if reg and tgt:
                tv = int(tgt.group(1), 16)
                if regions.in_flash(tv):
                    pend[reg.group(1)] = ("flash", tv)
                elif regions.in_iram(tv):
                    pend[reg.group(1)] = ("iram", tgt.group(2))
                else:
                    pend.pop(reg.group(1), None)
        elif ins.startswith("callx"):
            reg = rest.strip().split()[0] if rest.strip() else ""
            if reg in pend:
                calls[cur[1]].add(pend[reg])
        elif ins in DIRECT_JUMPS:
            for tv_s, name in re.findall(r"([0-9a-f]{8})\s*<([^>]+)>", rest):
                tv = int(tv_s, 16)
                if regions.in_flash(tv):
                    calls[cur[1]].add(("flash", tv))
                elif regions.in_iram(tv):
                    calls[cur[1]].add(("iram", name))

    seeds = [n for n in calls if any(s in n for s in ISR_SEEDS)]
    if not seeds:
        return None, f"ISR seed(s) {ISR_SEEDS} not found in ELF"

    seen: set[str] = set()
    stack = list(seeds)
    flash_hits: list[tuple[str, int]] = []
    while stack:
        fn = stack.pop()
        if fn in seen:
            continue
        seen.add(fn)
        for kind, tgt in calls.get(fn, ()):
            if kind == "flash":
                flash_hits.append((fn, int(tgt)))
            elif tgt not in seen:
                stack.append(str(tgt))

    return {
        "seeds": seeds,
        "reachable": len(seen),
        "flash_hits": flash_hits,
        "iram_syms": len(regions.iram),
    }, None


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("elf")
    ap.add_argument("--target", choices=sorted(TOOLCHAINS))
    ap.add_argument("-q", "--quiet", action="store_true")
    args = ap.parse_args()

    if not os.path.isfile(args.elf):
        print(f"check_iram_isr_safety: no such ELF: {args.elf}", file=sys.stderr)
        return 2

    target = detect_target(args.elf, args.target)
    result, err = analyse(args.elf, target)
    if result is None:
        print(f"check_iram_isr_safety: cannot verify ({err})", file=sys.stderr)
        return 2

    hits = result["flash_hits"]
    if hits:
        print(
            f"ERROR: {len(hits)} flash-resident target(s) reachable from the "
            f"Wi-Fi ISR ({target}). The Wi-Fi ISR runs with the flash cache "
            f"disabled during OTA writes, so these are crash risks:",
            file=sys.stderr,
        )
        for fn, addr in sorted(set(hits))[:25]:
            print(f"  {fn} -> 0x{addr:08x}", file=sys.stderr)
        return 1

    if not args.quiet:
        print(
            f"IRAM/ISR check OK ({target}): {len(result['seeds'])} seed(s) "
            f"{result['seeds']}, {result['reachable']} IRAM functions reachable, "
            f"0 flash targets"
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())
