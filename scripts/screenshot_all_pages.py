#!/usr/bin/env python3
"""EHomeSystem 全页面截图工具 — PC端 + 移动端"""
import json, asyncio, websockets, urllib.request, os, time

CDP_URL = "http://127.0.0.1:9222"
FRONTEND = "http://localhost:5174"
BACKEND = "http://localhost:8082"
SCREENSHOT_DIR = "/home/sun/workspace/EHomeSystem/screenshots"
USERNAME = "admin"
PASSWORD = "ehome_dev_admin_2026"

# All routes from router/index.ts (auth-required pages only)
ROUTES = [
    ("/dashboard", "仪表盘"),
    ("/node", "节点列表"),
    ("/node/1", "节点详情"),          # may 404 if no node id=1
    ("/channel", "通道管理"),
    ("/edge-device", "边缘设备列表"),
    ("/edge-device/1", "边缘设备详情"),  # may 404
    ("/data", "数据面板"),
    ("/firmware", "固件管理"),
    ("/device-configs", "配置模板"),
    ("/monitor", "系统监控"),
    ("/profile", "个人设置"),
]

# Also screenshot error pages (登录页需在未登录状态单独采集，见 Step 1)
EXTRA_ROUTES = [
    ("/403", "无权限"),
    ("/nonexistent-page-404", "404页面"),
]

ALL_ROUTES = EXTRA_ROUTES + ROUTES

class CDPSession:
    def __init__(self, ws, session_id, target_id):
        self.ws = ws
        self.session_id = session_id
        self.target_id = target_id
        self.msg_id = 100
        self.pending = {}
        
    async def send(self, method, params=None):
        self.msg_id += 1
        msg = {
            "id": self.msg_id,
            "method": method,
            "params": params or {},
            "sessionId": self.session_id,
        }
        await self.ws.send(json.dumps(msg))
        # Wait for response with matching id
        while True:
            resp = json.loads(await self.ws.recv())
            if resp.get("id") == self.msg_id:
                if "error" in resp:
                    raise Exception(f"CDP error {method}: {resp['error']}")
                return resp.get("result", {})
            # Skip events

async def get_browser_ws():
    resp = urllib.request.urlopen(f"{CDP_URL}/json/version")
    info = json.loads(resp.read())
    return info["webSocketDebuggerUrl"]

async def create_tab(ws, url):
    """Create a new tab and return CDPSession"""
    mid = 1
    await ws.send(json.dumps({
        "id": mid,
        "method": "Target.createTarget",
        "params": {"url": url}
    }))
    # Read messages until we get the response
    while True:
        resp = json.loads(await asyncio.wait_for(ws.recv(), 120))
        if resp.get("id") == mid:
            target_id = resp["result"]["targetId"]
            break
    
    # Attach to target
    mid = 2
    await ws.send(json.dumps({
        "id": mid,
        "method": "Target.attachToTarget",
        "params": {"targetId": target_id, "flatten": True}
    }))
    while True:
        resp = json.loads(await ws.ws.recv() if hasattr(ws, 'ws') else await asyncio.wait_for(ws.recv(), 120))
        if resp.get("id") == mid:
            session_id = resp["result"]["sessionId"]
            break
        elif resp.get("method") == "Target.attachedToTarget":
            session_id = resp["params"]["sessionId"]
            # Don't break yet, wait for the actual response
    
    session = CDPSession(ws, session_id, target_id)
    await asyncio.sleep(2)  # Let page load
    return session

async def login_via_api_and_inject(ws_browser):
    """Login via API, then inject JWT into browser localStorage"""
    # Login via curl
    import subprocess
    result = subprocess.run(
        ["curl", "-s", "-X", "POST", f"{BACKEND}/api/v1/auth/login",
         "-H", "Content-Type: application/json",
         "-d", json.dumps({"username": USERNAME, "password": PASSWORD, "rememberMe": True})],
        capture_output=True, text=True
    )
    resp = json.loads(result.stdout)
    token = resp["data"]["token"]
    print(f"  Login OK, token: {token[:20]}...")
    return token

async def main():
    os.makedirs(f"{SCREENSHOT_DIR}/pc", exist_ok=True)
    os.makedirs(f"{SCREENSHOT_DIR}/mobile", exist_ok=True)
    
    ws_url = await get_browser_ws()
    print(f"Browser WS: {ws_url}")
    
    async with websockets.connect(ws_url, max_size=100*1024*1024) as ws:
        # Step 1: Create tab, go to login page, inject token
        print("\n=== Step 1: Setup authentication ===")
        
        # Create a tab for login
        mid = 1
        await ws.send(json.dumps({
            "id": mid,
            "method": "Target.createTarget",
            "params": {"url": f"{FRONTEND}/login"}
        }))
        target_id = None
        while True:
            resp = json.loads(await asyncio.wait_for(ws.recv(), 120))
            if resp.get("id") == mid:
                target_id = resp["result"]["targetId"]
                break
        
        print(f"  Created tab: {target_id}")
        
        # Attach
        mid = 2
        await ws.send(json.dumps({
            "id": mid,
            "method": "Target.attachToTarget",
            "params": {"targetId": target_id, "flatten": True}
        }))
        session_id = None
        while True:
            resp = json.loads(await asyncio.wait_for(ws.recv(), 120))
            if resp.get("id") == mid:
                session_id = resp["result"]["sessionId"]
                break
        
        print(f"  Session: {session_id}")
        await asyncio.sleep(3)  # Let login page load
        
        # Enable Page and Runtime
        for method in ["Page.enable", "Runtime.enable", "Network.enable"]:
            mid += 1
            await ws.send(json.dumps({
                "id": mid,
                "method": method,
                "params": {},
                "sessionId": session_id,
            }))
            while True:
                resp = json.loads(await asyncio.wait_for(ws.recv(), 120))
                if resp.get("id") == mid:
                    break
        
        # Step 1.5: Capture the real login page BEFORE injecting the token
        # (the router redirects authenticated users from /login to /dashboard,
        # so it must be screenshotted while unauthenticated)
        # Clear any token persisted by previous runs, then reload to hit the
        # login page in a clean unauthenticated state.
        mid += 1
        await ws.send(json.dumps({
            "id": mid,
            "method": "Runtime.evaluate",
            "params": {"expression": "localStorage.clear(); location.reload();"},
            "sessionId": session_id,
        }))
        while True:
            resp = json.loads(await asyncio.wait_for(ws.recv(), 120))
            if resp.get("id") == mid:
                break
        await asyncio.sleep(3)  # Let the login page reload

        async def capture(fname):
            mid_holder[0] += 1
            await ws.send(json.dumps({
                "id": mid_holder[0],
                "method": "Page.captureScreenshot",
                "params": {"format": "png", "captureBeyondViewport": True},
                "sessionId": session_id,
            }))
            while True:
                resp = json.loads(await asyncio.wait_for(ws.recv(), 120))
                if resp.get("id") == mid_holder[0]:
                    data = resp.get("result", {}).get("data", "")
                    if data:
                        import base64
                        with open(fname, "wb") as f:
                            f.write(base64.b64decode(data))
                        print(f"    Saved: {fname}")
                    break

        async def set_viewport(width, height, scale, mobile, ua=None):
            mid_holder[0] += 1
            params = {
                "width": width, "height": height,
                "deviceScaleFactor": scale, "mobile": mobile,
            }
            if ua:
                params["userAgent"] = ua
            await ws.send(json.dumps({
                "id": mid_holder[0],
                "method": "Emulation.setDeviceMetricsOverride",
                "params": params,
                "sessionId": session_id,
            }))
            while True:
                resp = json.loads(await asyncio.wait_for(ws.recv(), 120))
                if resp.get("id") == mid_holder[0]:
                    break

        mid_holder = [mid]
        MOBILE_UA = "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Mobile/15E148 Safari/604.1"
        DESKTOP_UA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

        print("\n=== Step 1.5: Login page screenshots (unauthenticated) ===")
        await set_viewport(1440, 900, 1, False)
        await asyncio.sleep(1)
        await capture(f"{SCREENSHOT_DIR}/pc/登录页.png")
        await set_viewport(375, 812, 3, True, MOBILE_UA)
        await asyncio.sleep(1)
        await capture(f"{SCREENSHOT_DIR}/mobile/登录页.png")
        await set_viewport(1440, 900, 1, False, DESKTOP_UA)
        mid = mid_holder[0]
        
        # Get JWT token
        token = await login_via_api_and_inject(ws)
        
        # Inject token into localStorage (key names: 'token' and 'user')
        inject_js = f"""
        (async () => {{
            localStorage.setItem('token', '{token}');
            localStorage.setItem('user', JSON.stringify({{id: 1, username: '{USERNAME}'}}));
            return 'token injected to localStorage';
        }})()
        """
        mid += 1
        await ws.send(json.dumps({
            "id": mid,
            "method": "Runtime.evaluate",
            "params": {"expression": inject_js, "returnByValue": True, "awaitPromise": True},
            "sessionId": session_id,
        }))
        while True:
            resp = json.loads(await asyncio.wait_for(ws.recv(), 120))
            if resp.get("id") == mid:
                print(f"  Inject: {resp.get('result', {}).get('result', {}).get('value', 'N/A')}")
                break
        
        # Now navigate to dashboard to verify auth works
        mid += 1
        await ws.send(json.dumps({
            "id": mid,
            "method": "Page.navigate",
            "params": {"url": f"{FRONTEND}/dashboard"},
            "sessionId": session_id,
        }))
        while True:
            resp = json.loads(await asyncio.wait_for(ws.recv(), 120))
            if resp.get("id") == mid:
                break
        
        await asyncio.sleep(3)
        
        # Check current URL
        mid += 1
        await ws.send(json.dumps({
            "id": mid,
            "method": "Runtime.evaluate",
            "params": {"expression": "window.location.href"},
            "sessionId": session_id,
        }))
        while True:
            resp = json.loads(await asyncio.wait_for(ws.recv(), 120))
            if resp.get("id") == mid:
                cur_url = resp.get("result", {}).get("result", {}).get("value", "")
                print(f"  Current URL after redirect: {cur_url}")
                break
        
        # Step 2: PC screenshots (1440x900)
        print("\n=== Step 2: PC screenshots (1440x900) ===")
        mid += 1
        await ws.send(json.dumps({
            "id": mid,
            "method": "Emulation.setDeviceMetricsOverride",
            "params": {
                "width": 1440,
                "height": 900,
                "deviceScaleFactor": 1,
                "mobile": False,
            },
            "sessionId": session_id,
        }))
        while True:
            resp = json.loads(await asyncio.wait_for(ws.recv(), 120))
            if resp.get("id") == mid:
                break
        
        for path, name in ALL_ROUTES:
            url = f"{FRONTEND}{path}"
            safe_name = name.replace("/", "_")
            print(f"  PC: {name} ({path})")
            
            # Navigate
            mid += 1
            await ws.send(json.dumps({
                "id": mid,
                "method": "Page.navigate",
                "params": {"url": url},
                "sessionId": session_id,
            }))
            while True:
                resp = json.loads(await asyncio.wait_for(ws.recv(), 120))
                if resp.get("id") == mid:
                    break
            
            await asyncio.sleep(3)
            
            # Take screenshot
            mid += 1
            await ws.send(json.dumps({
                "id": mid,
                "method": "Page.captureScreenshot",
                "params": {"format": "png", "captureBeyondViewport": True},
                "sessionId": session_id,
            }))
            while True:
                resp = json.loads(await asyncio.wait_for(ws.recv(), 120))
                if resp.get("id") == mid:
                    data = resp.get("result", {}).get("data", "")
                    if data:
                        fname = f"{SCREENSHOT_DIR}/pc/{safe_name}.png"
                        with open(fname, "wb") as f:
                            import base64
                            f.write(base64.b64decode(data))
                        print(f"    Saved: {fname}")
                    break
        
        # Step 3: Mobile screenshots (375x812 - iPhone X)
        print("\n=== Step 3: Mobile screenshots (375x812) ===")
        mid += 1
        await ws.send(json.dumps({
            "id": mid,
            "method": "Emulation.setDeviceMetricsOverride",
            "params": {
                "width": 375,
                "height": 812,
                "deviceScaleFactor": 3,
                "mobile": True,
                "userAgent": "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Mobile/15E148 Safari/604.1",
            },
            "sessionId": session_id,
        }))
        while True:
            resp = json.loads(await asyncio.wait_for(ws.recv(), 120))
            if resp.get("id") == mid:
                break
        
        for path, name in ALL_ROUTES:
            url = f"{FRONTEND}{path}"
            safe_name = name.replace("/", "_")
            print(f"  Mobile: {name} ({path})")
            
            # Navigate
            mid += 1
            await ws.send(json.dumps({
                "id": mid,
                "method": "Page.navigate",
                "params": {"url": url},
                "sessionId": session_id,
            }))
            while True:
                resp = json.loads(await asyncio.wait_for(ws.recv(), 120))
                if resp.get("id") == mid:
                    break
            
            await asyncio.sleep(3)
            
            # Take screenshot
            mid += 1
            await ws.send(json.dumps({
                "id": mid,
                "method": "Page.captureScreenshot",
                "params": {"format": "png", "captureBeyondViewport": True},
                "sessionId": session_id,
            }))
            while True:
                resp = json.loads(await asyncio.wait_for(ws.recv(), 120))
                if resp.get("id") == mid:
                    data = resp.get("result", {}).get("data", "")
                    if data:
                        fname = f"{SCREENSHOT_DIR}/mobile/{safe_name}.png"
                        with open(fname, "wb") as f:
                            import base64
                            f.write(base64.b64decode(data))
                        print(f"    Saved: {fname}")
                    break
        
        # Cleanup: close tab
        mid += 1
        await ws.send(json.dumps({
            "id": mid,
            "method": "Target.closeTarget",
            "params": {"targetId": target_id},
        }))
        while True:
            resp = json.loads(await asyncio.wait_for(ws.recv(), 120))
            if resp.get("id") == mid:
                break
    
    print("\n=== Done! ===")
    print(f"PC screenshots: {SCREENSHOT_DIR}/pc/")
    print(f"Mobile screenshots: {SCREENSHOT_DIR}/mobile/")

if __name__ == "__main__":
    asyncio.run(main())
