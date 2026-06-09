const http = require('http');
const fs = require('fs');
const path = require('path');

const PORT = 80;
const BACKEND = process.env.BACKEND_URL || 'http://backend:8080';
const STATIC_DIR = '/app/dist';

const MIME = {
  '.html': 'text/html', '.js': 'application/javascript', '.css': 'text/css',
  '.json': 'application/json', '.png': 'image/png', '.svg': 'image/svg+xml',
  '.ico': 'image/x-icon', '.woff2': 'font/woff2', '.woff': 'font/woff',
  '.ttf': 'font/ttf', '.map': 'application/json',
};

const server = http.createServer((req, res) => {
  // API 请求代理到后端
  if (req.url.startsWith('/api/') || req.url.startsWith('/ping')) {
    const url = new URL(req.url, BACKEND);
    // 确保 Authorization header 不被丢弃
    const proxyHeaders = { ...req.headers, host: url.host };
    const proxyReq = http.request(url, {
      method: req.method,
      headers: proxyHeaders,
    }, (proxyRes) => {
      // WebSocket upgrade 支持
      res.writeHead(proxyRes.statusCode, proxyRes.headers);
      proxyRes.pipe(res);
    });
    proxyReq.on('error', () => {
      res.writeHead(502);
      res.end('Bad Gateway');
    });
    req.pipe(proxyReq);
    return;
  }

  // 静态文件
  let filePath = path.join(STATIC_DIR, req.url === '/' ? 'index.html' : req.url);
  const ext = path.extname(filePath);

  fs.readFile(filePath, (err, data) => {
    if (err) {
      // SPA fallback: 所有非文件请求返回 index.html
      fs.readFile(path.join(STATIC_DIR, 'index.html'), (e2, d2) => {
        if (e2) { res.writeHead(404); res.end('Not Found'); return; }
        res.writeHead(200, { 'Content-Type': 'text/html' });
        res.end(d2);
      });
      return;
    }
    res.writeHead(200, { 'Content-Type': MIME[ext] || 'application/octet-stream' });
    res.end(data);
  });
});

// WebSocket 代理 — 使用 http.request 转发 ws:// 会导致 ERR_INVALID_PROTOCOL
// 直接在 upgrade 事件中转发到后端
server.on('upgrade', (req, socket, head) => {
  // Forward the raw upgrade request to the backend
  const wsUrl = `ws://${new URL(BACKEND).host}${req.url}`;
  const options = new URL(wsUrl);
  const proxyReq = http.request({
    hostname: options.hostname,
    port: options.port,
    path: options.pathname + options.search,
    method: 'GET',
    headers: req.headers,
    protocol: 'http:',
  });
  proxyReq.on('upgrade', (proxyRes, proxySocket) => {
    socket.write('HTTP/1.1 101 Switching Protocols\r\n' +
      Object.entries(proxyRes.headers).map(([k,v]) => `${k}: ${v}`).join('\r\n') + '\r\n\r\n');
    proxySocket.pipe(socket);
    socket.pipe(proxySocket);
  });
  proxyReq.on('error', (err) => {
    console.error('WS proxy error:', err.message);
    socket.destroy();
  });
  proxyReq.end();
});

server.listen(PORT, () => console.log(`Frontend listening on :${PORT}, API proxy → ${BACKEND}`));
