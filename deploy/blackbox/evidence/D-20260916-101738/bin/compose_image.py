import json, sys
cfg = json.load(sys.stdin)
proj = cfg.get("name") or "ehome-bb"
for s, v in (cfg.get("services") or {}).items():
    img = v.get("image")
    if not img and v.get("build") is not None:
        img = "%s-%s" % (proj, s)
    if img:
        print("%s\t%s" % (s, img))
