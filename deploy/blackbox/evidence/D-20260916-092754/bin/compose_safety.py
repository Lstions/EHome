import json, sys
PROTECTED_NAMES = {"ehome-postgres","ehome-emqx","ehome-web","ehome-prometheus",
                   "ehome-alertmanager","nginx","ddns-go"}
PROTECTED_PORTS = {80, 3080, 8082, 5432, 1883, 18083}
def main():
    try:
        cfg = json.load(sys.stdin)
    except Exception as e:
        print("UNSAFE: cannot parse compose config JSON: %s" % e); return 3
    bad = []
    if cfg.get("name") != "ehome-bb":
        bad.append("project name=%r (expected ehome-bb)" % cfg.get("name"))
    svcs = cfg.get("services") or {}
    if not svcs:
        bad.append("no services after resolution")
    for s, v in svcs.items():
        cn = v.get("container_name")
        if cn in PROTECTED_NAMES:
            bad.append("service %s container_name=%s collides with shared container" % (s, cn))
        for p in (v.get("ports") or []):
            pub = p.get("published")
            try: pub = int(pub)
            except Exception: pub = None
            if pub in PROTECTED_PORTS:
                bad.append("service %s publishes protected host port %s" % (s, pub))
    SHARED_NETS = {"ehomesystem_default", "digital-family-tree_default"}
    for n, v in (cfg.get("networks") or {}).items():
        nm = v.get("name") or n
        if nm in SHARED_NETS and not v.get("external"):
            bad.append("network %s -> shared %s without external:true" % (n, nm))
    if bad:
        print("UNSAFE: " + " | ".join(bad)); return 3
    print("OK: services=%s" % ",".join(sorted(svcs)))
    return 0
sys.exit(main())
