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
    const proxyReq = http.request(url, {
      method: req.method,
      headers: { ...req.headers, host: url.host },
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

// WebSocket 代理
server.on('upgrade', (req, socket, head) => {
  const url = new URL(req.url, BACKEND.replace('http', 'ws'));
  const proxyReq = http.request(url, {
    method: 'GET',
    headers: req.headers,
  });
  proxyReq.on('upgrade', (proxyRes, proxySocket) => {
    socket.write('HTTP/1.1 101 Switching Protocols\r\n' +
      Object.entries(proxyRes.headers).map(([k,v]) => `${k}: ${v}`).join('\r\n') + '\r\n\r\n');
    proxySocket.pipe(socket);
    socket.pipe(proxySocket);
  });
  proxyReq.on('error', () => socket.destroy());
  proxyReq.end();
});

server.listen(PORT, () => console.log(`Frontend listening on :${PORT}, API proxy → ${BACKEND}`));
