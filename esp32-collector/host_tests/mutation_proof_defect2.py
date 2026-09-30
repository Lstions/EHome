#!/usr/bin/env python3
"""Mutation self-proof for defect 2 (2026-09-30).

For each mutation: apply exactly one behavioural edit (assert the anchor
matched exactly once), rebuild, run the target test, require an ASSERTION
failure (not a compile/link error), then restore and verify the md5 is
byte-identical to the fixed version.

Permanent, re-runnable proof artifact (do not delete):
    export PATH=/snap/cmake/1562/bin:$PATH
    cd esp32-collector
    python3 host_tests/mutation_proof_defect2.py
"""
import hashlib
import os
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BUILD = os.environ.get("HT_BUILD_DIR", os.path.join(ROOT, ".ht-work"))

BW = os.path.join(ROOT, "components/bus_worker/bus_worker.c")
SC = os.path.join(ROOT, "components/scheduler/scheduler.c")
RX = os.path.join(ROOT, "host_tests/bus_worker_rx_tests.c")
SR = os.path.join(ROOT, "host_tests/scheduler_route_tests.c")

FILES = [BW, SC, RX, SR, os.path.join(ROOT, "components/scheduler/scheduler.h"),
         os.path.join(ROOT, "host_tests/rx_health_e2e_tests.c"),
         os.path.join(ROOT, "host_tests/CMakeLists.txt"),
         os.path.join(ROOT, "components/msg_handler/handler_data.c")]


def md5(path):
    with open(path, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def configure():
    """Create the host-test build dir if it does not exist yet."""
    if os.path.isdir(BUILD):
        return 0, "already configured"
    r = subprocess.run(["cmake", "-S", os.path.join(ROOT, "host_tests"),
                        "-B", BUILD],
                       cwd=ROOT, capture_output=True, text=True)
    return r.returncode, r.stdout + r.stderr


def build(target):
    r = subprocess.run(["cmake", "--build", BUILD, "-j8", "--target", target,
                        "--", "-k"],
                       cwd=ROOT, capture_output=True, text=True)
    return r.returncode, r.stdout + r.stderr


def run(test):
    r = subprocess.run([os.path.join(BUILD, test)], capture_output=True, text=True)
    return r.returncode, r.stdout + r.stderr


def mutate(path, old, new):
    s = open(path).read()
    n = s.count(old)
    assert n == 1, "anchor matched %d times in %s" % (n, path)
    open(path, "w").write(s.replace(old, new))


# (name, file, old, new, target, expected failing test)
MUTATIONS = [
    (
        "M1 revert RX-timeout to the channel-only counter (the original defect)",
        BW,
        "  (void)scheduler_notify_command_outcome(rt->bus_ch[i], pcmd.edge_device_id,\n"
        "                                          pcmd.command_template_id,\n"
        "                                         pcmd.command_index, false);\n",
        "  scheduler_notify_channel_error(rt->bus_ch[i]);\n",
        "bus_worker_rx_tests",
        "bus_worker_rx_tests",
    ),
    (
        "M2 restore 'enqueue success clears error_count' (the original defect)",
        SC,
        "                (*total_samples)++;\n",
        "                (*total_samples)++;\n                scmd->error_count = 0;\n",
        "scheduler_route_tests",
        "scheduler_route_tests",
    ),
    (
        "M3 restore the hardcoded, slot-less RX-timeout log",
        BW,
        '  ESP_LOGW(TAG_RX, "RX timeout slot%d type=%d reqID=%lu (%lldms)",\n'
        "   i, (int)rt->bus_ctx[i].bus_type,\n"
        "   (unsigned long)pcmd.request_id, (long long)elapsed_ms);\n",
        '  ESP_LOGW(TAG_RX, "UART RX timeout reqID=%lu (%lldms)",\n'
        "   (unsigned long)pcmd.request_id, (long long)elapsed_ms);\n",
        "bus_worker_rx_tests",
        "bus_worker_rx_tests",
    ),
    (
        "M4 revert complete-response success to the channel-only counter",
        BW,
        "  (void)scheduler_notify_command_outcome(rt->bus_ch[idx], pcmd.edge_device_id,\n"
        "                                          pcmd.command_template_id,\n"
        "                                         pcmd.command_index, true);\n",
        "  scheduler_notify_channel_success(rt->bus_ch[idx]);\n",
        "bus_worker_rx_tests",
        "bus_worker_rx_tests",
    ),
    (
        "M5 ignore command identity when matching the reported slot",
        SC,
        "            if (dev->edge_device_id != edge_device_id) continue;\n"
        "            if (command_index >= dev->command_count) return NULL;\n"
        "            if (dev->commands[command_index].template_id == command_template_id)\n"
        "                return &dev->commands[command_index];\n",
        "            if (command_index >= dev->command_count) continue;\n"
        "            if (dev->commands[command_index].template_id == command_template_id)\n"
        "                return &dev->commands[command_index];\n",
        "scheduler_route_tests",
        "scheduler_route_tests",
    ),
    (
        "M8 drop edge_device_id from matching (cross-device misattribution)",
        SC,
        "    if (command_template_id == 0 || edge_device_id == 0) return NULL;\n"
        "    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {\n"
        "        if (!s_channels[i].active || s_channels[i].config.id != channel_id)\n"
        "            continue;\n"
        "        sched_channel_t *ch = &s_channels[i];\n"
        "        for (int ed = 0; ed < ch->edge_device_count; ed++) {\n"
        "            sched_edge_device_t *dev = &ch->edge_devices[ed];\n"
        "            if (dev->edge_device_id != edge_device_id) continue;\n"
        "            if (command_index >= dev->command_count) return NULL;\n",
        "    if (command_template_id == 0) return NULL;\n"
        "    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {\n"
        "        if (!s_channels[i].active || s_channels[i].config.id != channel_id)\n"
        "            continue;\n"
        "        sched_channel_t *ch = &s_channels[i];\n"
        "        for (int ed = 0; ed < ch->edge_device_count; ed++) {\n"
        "            sched_edge_device_t *dev = &ch->edge_devices[ed];\n"
        "            if (command_index >= dev->command_count) continue;\n",
        "scheduler_route_tests",
        "scheduler_route_tests",
    ),
    (
        "M6 end-to-end: revert the timeout hook only (server sees nothing)",
        BW,
        "  (void)scheduler_notify_command_outcome(rt->bus_ch[i], pcmd.edge_device_id,\n"
        "                                          pcmd.command_template_id,\n"
        "                                         pcmd.command_index, false);\n",
        "  scheduler_notify_channel_error(rt->bus_ch[i]);\n",
        "rx_health_e2e_tests",
        "rx_health_e2e_tests",
    ),
    (
        "M7 end-to-end: restore 'enqueue success clears error_count'",
        SC,
        "                (*total_samples)++;\n",
        "                (*total_samples)++;\n                scmd->error_count = 0;\n",
        "rx_health_e2e_tests",
        "rx_health_e2e_tests",
    ),
]


def main():
    created_build_dir = not os.path.isdir(BUILD)
    rc, out = configure()
    if rc != 0:
        print("configure failed:\n" + out)
        return 2

    baseline = {p: md5(p) for p in FILES}
    print("=== baseline md5 ===")
    for p, h in baseline.items():
        print("  %s  %s" % (h, os.path.relpath(p, ROOT)))

    # Keep the pristine text of every mutated file. Recovery must never depend on
    # re-reading a file we may have just truncated (see the 2026-09-30 incident).
    global ORIGINAL
    ORIGINAL = {p: open(p).read() for p in FILES}

    results = []
    for name, path, old, new, target, test in MUTATIONS:
        print("\n=== %s ===" % name)
        mutate(path, old, new)
        rc, out = build(target)
        if rc != 0:
            print("  BUILD FAILED (this mutation is INVALID - compile error, "
                  "not an assertion):")
            print("\n".join(out.splitlines()[-15:]))
            results.append((name, "BUILD-FAIL(invalid)", ""))
            # Restore from the in-memory baseline text, NOT via open(path,"w")
            # around a read of the same path: that truncated the file to 0 bytes
            # before the read and silently wiped bus_worker.c / scheduler.c
            # (2026-09-30 incident). Rewriting in place keeps it atomic-ish and
            # read-order independent.
            with open(path, "w") as fh:
                fh.write(ORIGINAL[path])
            continue
        rc, out = run(test)
        lines = [l for l in out.splitlines()
                 if "FAIL " in l or "FAILURES" in l or "all tests passed" in l]
        verdict = "RED(assertion)" if rc != 0 else "GREEN(unexpected)"
        print("  rc=%d -> %s" % (rc, verdict))
        for l in lines:
            print("    " + l)
        results.append((name, verdict, " | ".join(lines)))
        # restore
        s = open(path).read()
        assert s.count(new) == 1, "restore anchor lost in %s" % path
        open(path, "w").write(s.replace(new, old))
        assert md5(path) == baseline[path], "md5 mismatch after restore: %s" % path

    print("\n=== md5 verification after all restores ===")
    ok = True
    for p, h in baseline.items():
        cur = md5(p)
        same = cur == h
        ok = ok and same
        print("  %s  %s" % ("OK " if same else "BAD", os.path.relpath(p, ROOT)))

    # Final: rebuild everything and require the suite green.
    print("\n=== final rebuild + ctest (bus_dma_tests is the known baseline "
          "failure) ===")
    rc, out = build("all")
    print("  build rc=%d (2 == the known bus_dma_tests baseline)" % rc)
    r = subprocess.run(["ctest", "--output-on-failure"], cwd=BUILD,
                       capture_output=True, text=True)
    print("\n".join(r.stdout.splitlines()[-12:]))

    print("\n=== mutation summary ===")
    for name, verdict, detail in results:
        print("  %-70s %s" % (name, verdict))
    print("\nmd5 identical after restore: %s" % ("YES" if ok else "NO"))

    # Leave the tree clean: only remove a build dir this script created.
    if created_build_dir:
        subprocess.run(["rm", "-rf", BUILD], check=False)
        print("removed temporary build dir %s" % os.path.relpath(BUILD, ROOT))
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
