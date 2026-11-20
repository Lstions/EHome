(function(){
  "use strict";
  var $ = function(s, r){ return (r || document).querySelector(s); };
  var $$ = function(s, r){ return Array.prototype.slice.call((r || document).querySelectorAll(s)); };
  var esc = function(v){ return String(v === null || v === undefined ? "" : v).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#39;"); };

  var NODE = { id: "F0F5BD02FDD84", fw: "2.8.0", proto: "2.6" };
  var BUS = {
    i2c: { label: "I2C", resources: [
      { id: "I2C0", enabled: true, pins: "SDA21 / SCL22", mode: "主机", params: "1MHz", dma: null, channels: [] }
    ]},
    uart: { label: "UART", resources: [
      { id: "UART0", enabled: true, pins: "TX43 / RX44", mode: "异步", params: "115200 8N1", dma: 0, channels: [
        { id: 101, name: "RS485 采集", params: "9600 8N1", devices: 2, enabled: true }
      ]},
      { id: "UART1", enabled: true, pins: "TX17 / RX18", mode: "异步", params: "9600 8N1", dma: null, channels: [
        { id: 103, name: "逆变器", params: "9600 8N1", devices: 1, enabled: true }
      ]}
    ]},
    spi: { label: "SPI", resources: [
      { id: "SPI2", enabled: true, pins: "MOSI11 / MISO13 / SCLK12 / CS10", mode: "主机", params: "1MHz", dma: 2, channels: [
        { id: 105, name: "温湿度", params: "1MHz / CS10", devices: 1, enabled: true }
      ]}
    ]},
    adc: { label: "ADC", resources: [
      { id: "ADC1", enabled: true, pins: "GPIO34", mode: "输入", params: "12bit / 11dB", dma: null, channels: [
        { id: 107, name: "电池采样", params: "12bit / 11dB", devices: 0, enabled: false }
      ]}
    ]}
  };
  var DMA = [
    { id: 0, name: "GDMA_CH0", caps: "发送 · 接收 · 突发", burst: 4096, state: "allocated", bound: "UART0", bus: "UART" },
    { id: 1, name: "GDMA_CH1", caps: "发送 · 接收", burst: 2048, state: "free", bound: "", bus: "UART / I2C / SPI" },
    { id: 2, name: "GDMA_CH2", caps: "发送 · 接收 · 突发", burst: 4096, state: "allocated", bound: "SPI2", bus: "UART / SPI" },
    { id: 3, name: "GDMA_CH3", caps: "发送", burst: 1024, state: "disabled", bound: "", bus: "UART / I2C" }
  ];
  var DEVICES = [
    { id: 1, name: "电表 SN3001", type: "电表", channel: "RS485 采集", res: "UART0", addr: "1", readings: ["电压 220.3V", "功率 1.24kW"], status: "online", last: "3 秒前" },
    { id: 2, name: "BMS 嘉佰达", type: "BMS", channel: "RS485 采集", res: "UART0", addr: "2", readings: ["总压 52.1V", "SOC 86%"], status: "online", last: "5 秒前" },
    { id: 3, name: "逆变器 Techfine", type: "逆变器", channel: "逆变器", res: "UART1", addr: "1", readings: [], status: "offline", last: "2 小时前" },
    { id: 4, name: "温湿度 LKTH01", type: "温湿度", channel: "温湿度", res: "SPI2", addr: "CS10", readings: [], status: "pending", last: "—" }
  ];
  var OTA = [
    { id: 9, from: "2.8.0", to: "2.9.0", status: "downloading", progress: 42, start: "2026/11/19 21:12:04", end: "—", forced: false },
    { id: 8, from: "2.7.1", to: "2.8.0", status: "success", progress: 100, start: "2026/11/16 10:02:11", end: "7 分 12 秒", forced: false },
    { id: 7, from: "2.7.0", to: "2.7.1", status: "failed", progress: 63, start: "2026/11/12 09:20:33", end: "3 分 08 秒", forced: true, reason: "现场回滚验证" },
    { id: 6, from: "2.6.9", to: "2.7.0", status: "success", progress: 100, start: "2026/11/08 14:40:02", end: "6 分 51 秒", forced: false },
    { id: 5, from: "2.6.8", to: "2.6.9", status: "timeout", progress: 88, start: "2026/11/03 19:02:44", end: "10 分 00 秒", forced: false }
  ];
  var GPIO = [
    { pin: 4, label: "继电器 1", dir: "输出", level: 1, configured: true, owner: "" },
    { pin: 5, label: "门磁", dir: "输入（上拉）", level: 0, configured: true, owner: "" },
    { pin: 6, label: "", dir: "—", level: null, configured: false, owner: "" },
    { pin: 7, label: "", dir: "—", level: null, configured: false, owner: "UART1" },
    { pin: 8, label: "水位", dir: "输入", level: 1, configured: true, owner: "" },
    { pin: 9, label: "", dir: "—", level: null, configured: false, owner: "" },
    { pin: 10, label: "", dir: "—", level: null, configured: false, owner: "SPI2" },
    { pin: 11, label: "", dir: "—", level: null, configured: false, owner: "" }
  ];
  var PWM = [
    { id: "PWM0", label: "LED 调光", freq: "5 kHz", bits: 12, running: true, duty: 42, configured: true },
    { id: "PWM1", label: "风扇", freq: "25 kHz", bits: 10, running: false, duty: 0, configured: true },
    { id: "PWM2", label: "", freq: "—", bits: null, running: null, duty: null, configured: false },
    { id: "PWM3", label: "", freq: "—", bits: null, running: null, duty: null, configured: false }
  ];
  var LOGS = [
    { ts: "21:18:02.114", lv: "I", tag: "wifi", msg: "RSSI -62 dBm, channel 6" },
    { ts: "21:18:02.092", lv: "I", tag: "mqtt", msg: "publish status seq=18422 qos=1" },
    { ts: "21:18:01.870", lv: "D", tag: "bus", msg: "uart0 rx 12B: 01 03 04 00 DC 00 7B" },
    { ts: "21:18:01.422", lv: "W", tag: "i2c", msg: "clock stretch timeout on 0x76, retry 1/3" },
    { ts: "21:18:00.998", lv: "I", tag: "sched", msg: "channel 101 next run in 5000 ms" },
    { ts: "21:17:59.640", lv: "E", tag: "adc", msg: "sample overrange ch3, raw=4095" },
    { ts: "21:17:58.201", lv: "I", tag: "ota", msg: "idle, current slot app0 valid" },
    { ts: "21:17:55.114", lv: "I", tag: "heap", msg: "free=112708 largest=118784 internal" },
    { ts: "21:17:54.002", lv: "D", tag: "periph", msg: "gpio21 level=1 after write" },
    { ts: "21:17:52.883", lv: "I", tag: "status", msg: "uptime=17103s epoch=42 sync=in_sync" }
  ];
  (function(){
    var tags = ["bus", "sched", "mqtt", "heap", "periph", "i2c", "status", "adc"];
    var msgs = [
      "uart0 tx 8B ok",
      "channel 103 next run in 10000 ms",
      "publish telemetry seq=18421 qos=0",
      "free=113240 largest=118784 internal",
      "gpio4 write level=1 ack",
      "i2c0 transaction ok addr=0x76",
      "status report sent, rssi=-63",
      "adc1 sample ch0 raw=2048"
    ];
    for (var i = 0; i < 26; i++) {
      var sec = 50 - i;
      var ss = sec < 10 ? "0" + sec : String(sec);
      LOGS.push({ ts: "21:17:" + ss + ".4" + (i % 10) + "1", lv: i % 7 === 0 ? "W" : i % 5 === 0 ? "D" : "I", tag: tags[i % tags.length], msg: msgs[i % msgs.length] });
    }
  })();

  var TERM = {
    "UART0-101": [
      { dir: "tx", t: "21:18:02.114", n: 8, data: "01 03 00 00 00 02 C4 0B" },
      { dir: "rx", t: "21:18:02.180", n: 9, data: "01 03 04 00 DC 00 7B 3A 71" },
      { dir: "tx", t: "21:18:07.116", n: 8, data: "01 03 00 00 00 02 C4 0B" },
      { dir: "rx", t: "21:18:07.188", n: 9, data: "01 03 04 00 DB 00 7A 3B 72" },
      { dir: "rx", t: "21:18:08.002", n: 5, data: "02 03 02 00 34", err: "ERR:04" }
    ],
    "UART1-103": [
      { dir: "tx", t: "21:17:41.220", n: 8, data: "01 03 00 10 00 02 C5 0E" },
      { dir: "rx", t: "21:17:41.402", n: 9, data: "01 03 04 14 6B 00 00 8B 2F" }
    ],
    "SPI2-105": [
      { dir: "tx", t: "21:17:20.010", n: 4, data: "AA 55 00 01" },
      { dir: "rx", t: "21:17:20.016", n: 6, data: "AA 55 06 18 F4 51" }
    ]
  };

  (function(){
    var older = [];
    for (var i = 0; i < 14; i++) {
      var sec = 55 - i;
      var ss = sec < 10 ? "0" + sec : String(sec);
      older.push({ dir: i % 2 === 0 ? "tx" : "rx", t: "21:17:" + ss + ".220", n: 8, data: i % 2 === 0 ? "01 03 00 00 00 02 C4 0B" : "01 03 04 00 DA 00 79 3B 73" });
    }
    TERM["UART0-101"] = older.reverse().concat(TERM["UART0-101"]);
  })();
  var state = { tab: "bus", bus: "uart", res: "UART0", resQuery: "", scan: null, periph: "gpio", periphFilter: "all", dmaFilter: "all", devQuery: "", devChan: "", devStatus: "", devView: "list", otaFilter: "all", logTab: "realtime", logPaused: false, term: "UART0-101", hex: "hex" };
  var currentSubmit = null;
  var confirmCallback = null;
  var toastTimer = null;

  function toast(msg, ok){
    var el = $("#toast");
    el.innerHTML = (ok ? "<span class='ok'>✓</span>" : "") + esc(msg);
    el.classList.add("show");
    if (toastTimer) clearTimeout(toastTimer);
    toastTimer = setTimeout(function(){ el.classList.remove("show"); }, 2400);
  }
  function openDrawer(opts){
    $("#drawerTitle").textContent = opts.title;
    $("#drawerSub").textContent = opts.sub || "";
    $("#drawerBody").innerHTML = opts.body;
    $("#drawerSubmit").textContent = opts.submit || "保存";
    currentSubmit = opts.onSubmit || null;
    $("#drawer").classList.add("show");
    $("#drawerMask").classList.add("show");
  }
  function closeDrawer(){
    $("#drawer").classList.remove("show");
    $("#drawerMask").classList.remove("show");
    currentSubmit = null;
  }
  function askConfirm(title, body, okText, cb){
    $("#confirmTitle").textContent = title;
    $("#confirmBody").textContent = body;
    $("#confirmOk").textContent = okText || "确认";
    confirmCallback = cb;
    $("#confirm").classList.add("show");
    $("#confirmMask").classList.add("show");
  }
  function closeConfirm(){
    $("#confirm").classList.remove("show");
    $("#confirmMask").classList.remove("show");
    confirmCallback = null;
  }

  function busKeyOf(id){ return String(id).replace(/[0-9]+$/, "").toLowerCase(); }
  function findRes(type, id){
    var list = BUS[type] ? BUS[type].resources : [];
    for (var i = 0; i < list.length; i++) if (list[i].id === id) return list[i];
    return null;
  }
  function currentRes(){ return findRes(state.bus, state.res) || BUS[state.bus].resources[0]; }
  function dmaOf(id){
    for (var i = 0; i < DMA.length; i++) if (DMA[i].id === id) return DMA[i];
    return null;
  }
  function allResources(){
    var out = [];
    Object.keys(BUS).forEach(function(k){ BUS[k].resources.forEach(function(r){ out.push(r); }); });
    return out;
  }

  function renderBusSeg(){
    var html = "";
    ["i2c", "uart", "spi", "adc"].forEach(function(k){
      html += "<button class='seg-btn" + (state.bus === k ? " active" : "") + "' data-bus='" + k + "' role='tab'>" + BUS[k].label + "<span class='seg-num'>" + BUS[k].resources.length + "</span></button>";
    });
    $("#busSeg").innerHTML = html;
  }

  function renderResList(){
    var list = BUS[state.bus].resources.filter(function(r){
      if (!state.resQuery) return true;
      var q = state.resQuery.toLowerCase();
      return (r.id + " " + r.pins + " " + r.params).toLowerCase().indexOf(q) >= 0;
    });
    var html = "";
    list.forEach(function(r){
      var ch = r.channels.length;
      var dma = r.dma === null || r.dma === undefined ? "DMA 未绑定" : "DMA " + (dmaOf(r.dma) ? dmaOf(r.dma).name.replace("GDMA_", "") : "");
      html += "<button class='res-card" + (r.id === state.res ? " active" : "") + "' data-res='" + esc(r.id) + "'>";
      html += "<span class='res-top'><span class='res-name'>" + esc(r.id) + "</span><span class='res-state " + (r.enabled ? "on" : "off") + "'>" + (r.enabled ? "可用" : "禁用") + "</span></span>";
      html += "<div class='res-pins'>" + esc(r.pins) + "</div>";
      html += "<div class='res-meta'>" + esc(r.mode) + " · " + esc(r.params) + "</div>";
      html += "<div class='res-badges'><span class='mini-badge" + (ch ? " blue" : "") + "'>通道 " + ch + "</span><span class='mini-badge'>" + esc(dma) + "</span></div>";
      html += "</button>";
    });
    if (!html) html = "<div class='empty' style='padding:22px 10px'><b>没有匹配的资源</b><p>换个关键词，或点「查询资源」让设备重新上报。</p></div>";
    $("#resList").innerHTML = html;
    $("#resCount").textContent = list.length;
  }

  function channelRows(r){
    if (!r.channels.length) return "";
    var html = "<div class='table-wrap'><table class='tbl'><thead><tr><th>通道</th><th>参数</th><th>边缘设备</th><th>状态</th><th class='acts'>操作</th></tr></thead><tbody>";
    r.channels.forEach(function(c){
      html += "<tr><td><div class='name'>" + esc(c.name) + "</div><div class='sub mono'>#" + c.id + " · " + esc(r.id) + "</div></td>";
      html += "<td class='mono'>" + esc(c.params) + "</td>";
      html += "<td>" + (c.devices ? c.devices + " 台" : "—") + "</td>";
      html += "<td><span class='pill " + (c.enabled ? "ok" : "muted") + "'>" + (c.enabled ? "正常" : "已停用") + "</span></td>";
      html += "<td class='acts'><button class='link-btn' data-edit-channel='" + c.id + "'>配置</button><button class='link-btn danger' data-del-channel='" + c.id + "'>删除</button></td></tr>";
    });
    return html + "</tbody></table></div>";
  }

  function renderDetail(){
    var r = currentRes();
    if (!r) { $("#resDetail").innerHTML = "<div class='empty'><b>该类型下没有资源</b><p>点右上角「查询资源」让节点重新上报。</p></div>"; return; }
    var chCount = r.channels.length;
    var dma = r.dma === null || r.dma === undefined ? null : dmaOf(r.dma);
    var options = "<option value=''" + (dma ? "" : " selected") + ">不使用 DMA</option>";
    DMA.forEach(function(d){
      var label = d.name.replace("GDMA_", "") + " · " + (d.state === "free" ? "空闲" : d.state === "allocated" ? "已分配给 " + d.bound : "已禁用");
      var disabled = d.state !== "free" && d.bound !== r.id;
      options += "<option value='" + d.id + "'" + (dma && d.id === dma.id ? " selected" : "") + (disabled ? " disabled" : "") + ">" + esc(label) + "</option>";
    });
    var html = "";
    html += "<div class='detail-head'><div><div class='detail-title'>" + esc(r.id) + "<span class='tag blue'>" + BUS[state.bus].label + " 资源</span><span class='pill " + (r.enabled ? "ok" : "muted") + "'>" + (r.enabled ? "可用" : "禁用") + "</span></div>";
    html += "<div class='detail-sub'>设备上报的物理资源 · 只读；通道会随配置下发到节点</div></div>";
    html += "<div class='detail-actions'>";
    if (state.bus === "i2c") html += "<button class='btn ghost' data-action='scan'><svg class='i i-sm'><use href='#i-scan'/></svg>地址扫描</button>";
    html += "<span class='dma-inline'>DMA<select class='select' id='resDma' aria-label='DMA 绑定'>" + options + "</select></span>";
    html += "</div></div>";
    html += "<div class='meta-strip'>";
    html += "<div><label>引脚</label><b class='mono'>" + esc(r.pins) + "</b></div>";
    html += "<div><label>工作模式</label><b>" + esc(r.mode) + "</b></div>";
    html += "<div><label>关键参数</label><b>" + esc(r.params) + "</b></div>";
    html += "<div><label>" + (state.bus === "uart" ? "当前波特率" : "已挂载通道") + "</label><b>" + (state.bus === "uart" ? (chCount ? esc(r.channels[0].params.split(" ")[0]) : "未配置（未创建通道）") : (chCount + " 条")) + "</b></div>";
    html += "</div>";
    if (state.scan && state.bus === "i2c") {
      html += "<div class='scan-result'><b>扫描完成：发现 " + state.scan.length + " 个地址</b>";
      state.scan.forEach(function(a){ html += "<button class='addr-chip' data-fill-addr='" + a + "'>" + a + " · 填入</button>"; });
      html += "<span class='muted'>点击地址可带入新建通道表单</span></div>";
    }
    html += "<div class='section-head'><h3>该资源下的通道</h3><span class='count'>" + chCount + " 条</span><button class='btn text' data-action='new-channel'><svg class='i i-sm'><use href='#i-plus'/></svg>新建通道</button></div>";
    if (chCount) {
      html += channelRows(r);
    } else {
      html += "<div class='empty'><span class='ic'><svg class='i i-lg'><use href='#i-link'/></svg></span><b>还没有通道</b><p>通道是这条资源上的采集实例。创建后会随配置下发，节点按通道采集数据。</p><button class='btn primary' data-action='new-channel'>在此资源上新建通道</button></div>";
    }
    var attached = DEVICES.filter(function(d){ return r.channels.some(function(c){ return c.name === d.channel; }); });
    if (attached.length) {
      html += "<div class='dev-strip'><span class='lbl'>挂载设备</span>";
      attached.forEach(function(d){ html += "<span class='dev-chip'><i class='dot " + (d.status === "online" ? "on" : "off") + "'></i>" + esc(d.name) + "<em>" + esc(d.addr) + "</em></span>"; });
      html += "<button class='link-btn' data-action='goto-devices'>在关联设备中查看</button></div>";
    }
    html += "<div class='detail-note'>资源名称、引脚与参数由设备上报，页面不能修改；DMA 绑定与通道配置属于可下发的配置项。</div>";
    $("#resDetail").innerHTML = html;
    $("#resDma").addEventListener("change", function(e){
      var val = e.target.value;
      r.dma = val === "" ? null : Number(val);
      renderResList(); renderDetail();
      toast(r.dma === null ? "已解绑 DMA" : "DMA 已绑定，配置将下发到节点", true);
    });
  }

  function renderBus(){ renderBusSeg(); renderResList(); renderDetail(); }

  function channelForm(res, ch){
    var isEdit = !!ch;
    var v = ch || {};
    var name = v.name || (state.bus === "i2c" ? res.id + " 采集" : res.id + " 通道");
    var html = "";
    html += "<div class='field'><label>通道名称</label><input id='fName' value='" + esc(name) + "'><div class='help'>留空时按资源标识自动命名</div></div>";
    if (state.bus === "i2c") {
      html += "<div class='grid2'><div class='field'><label>从机地址</label><input id='fAddr' placeholder='0x76' value='" + esc(v.addr || "") + "'><div class='help'>7 位地址，如 0x76</div></div>";
      html += "<div class='field'><label>时钟频率</label><select id='fRate'><option>100 kHz</option><option selected>400 kHz</option><option>1 MHz</option></select></div></div>";
    } else if (state.bus === "uart") {
      html += "<div class='grid2'><div class='field'><label>波特率</label><select id='fRate'><option>4800</option><option>9600</option><option>19200</option><option>38400</option><option>115200</option></select></div>";
      html += "<div class='field'><label>串口参数</label><select id='fFrame'><option>8N1</option><option>8E1</option><option>8O1</option></select></div></div>";
    } else if (state.bus === "spi") {
      html += "<div class='grid2'><div class='field'><label>CS 引脚</label><select id='fCs'><option>GPIO10</option><option>GPIO9</option></select></div>";
      html += "<div class='field'><label>时钟频率</label><select id='fRate'><option>1 MHz</option><option>4 MHz</option><option>10 MHz</option></select></div></div>";
    } else {
      html += "<div class='grid2'><div class='field'><label>衰减</label><select id='fAtt'><option>0 dB</option><option>2.5 dB</option><option>6 dB</option><option selected>11 dB</option></select></div>";
      html += "<div class='field'><label>位宽</label><select id='fBits'><option>9 bit</option><option>10 bit</option><option>11 bit</option><option selected>12 bit</option></select></div></div>";
    }
    html += "<div class='field'><label>启用</label><select id='fEnabled'><option value='1'>启用（下发后开始采集）</option><option value='0'>停用（保留配置）</option></select></div>";
    html += "<div class='form-note'>保存后会立即写入通道配置并向节点下发。节点离线时无法提交，请先确认设备在线。</div>";
    openDrawer({
      title: isEdit ? "配置通道" : "在 " + res.id + " 上新建通道",
      sub: isEdit ? "修改后会重新下发配置，采集会短暂中断" : "资源：" + res.id + " · " + BUS[state.bus].label,
      body: html,
      submit: isEdit ? "保存并下发" : "创建并下发",
      onSubmit: function(){
        var nm = $("#fName").value.trim() || name;
        var enabled = $("#fEnabled").value === "1";
        var rateEl = $("#fRate");
        var params = "";
        if (state.bus === "uart") params = (rateEl ? rateEl.value : "9600") + " " + ($("#fFrame") ? $("#fFrame").value : "8N1");
        else if (state.bus === "i2c") params = ($("#fAddr") && $("#fAddr").value ? $("#fAddr").value + " · " : "") + (rateEl ? rateEl.value : "400 kHz");
        else if (state.bus === "spi") params = (rateEl ? rateEl.value : "1 MHz") + " / " + ($("#fCs") ? $("#fCs").value : "GPIO10");
        else params = ($("#fAtt") ? $("#fAtt").value : "11 dB") + " / " + ($("#fBits") ? $("#fBits").value : "12 bit");
        if (isEdit) { ch.name = nm; ch.params = params; ch.enabled = enabled; }
        else res.channels.push({ id: Date.now() % 10000, name: nm, params: params, devices: 0, enabled: enabled });
        closeDrawer(); renderBus(); toast(isEdit ? "通道已更新，配置已下发" : "通道已创建，配置已下发", true);
      }
    });
    if (state.bus === "uart") $("#fRate").value = "9600";
  }

  function renderPeriphSummary(){
    var list = state.periph === "gpio" ? GPIO : PWM;
    var conf = list.filter(function(x){ return x.configured; }).length;
    var occupied = state.periph === "gpio" ? list.filter(function(x){ return !x.configured && x.owner; }).length : 0;
    var un = list.length - conf;
    $("#periphSum").innerHTML = "<span class='sum-item ok'><b>" + conf + "</b>已配置</span><span class='sum-item'><b>" + un + "</b>未配置</span>" + (state.periph === "gpio" ? "<span class='sum-item warn'><b>" + occupied + "</b>被占用</span>" : "");
    $("#periphBanner").innerHTML = "<div class='note-banner'><svg class='i i-sm'><use href='#i-info'/></svg>节点在线：可直接读写引脚与 PWM；配置变更会立即下发。被总线占用的引脚不可配置。</div>";
  }
  function gpioRows(){
    return GPIO.filter(function(g){
      if (state.periphFilter === "configured") return g.configured;
      if (state.periphFilter === "unconfigured") return !g.configured;
      return true;
    }).map(function(g){
      var level = g.level === null ? "<span class='gpio-level unknown'>—</span>" : g.level ? "<span class='gpio-level high'>高电平</span>" : "<span class='gpio-level low'>低电平</span>";
      var stateText = g.configured ? level : (g.owner ? "<span class='pill muted'>被 " + esc(g.owner) + " 占用</span>" : "<span class='pill muted'>未配置</span>");
      var ops = "";
      if (g.configured) {
        ops += g.dir.indexOf("输出") === 0 ? "<button class='switch " + (g.level ? "on" : "") + "' data-gpio-toggle='" + g.pin + "' aria-label='写入电平'></button>" : "<button class='link-btn' data-gpio-read='" + g.pin + "'>读取</button>";
        ops += "<button class='link-btn' data-gpio-config='" + g.pin + "'>配置</button>";
      } else if (!g.owner) {
        ops += "<button class='btn sm ghost' data-gpio-config='" + g.pin + "'>配置</button>";
      }
      return "<tr><td class='mono'>GPIO" + g.pin + "</td><td>" + (g.label ? esc(g.label) : "<span class='muted'>—</span>") + "</td><td>" + esc(g.dir) + "</td><td>" + stateText + "</td><td class='acts'>" + ops + "</td></tr>";
    }).join("");
  }
  function pwmRows(){
    return PWM.filter(function(p){
      if (state.periphFilter === "configured") return p.configured;
      if (state.periphFilter === "unconfigured") return !p.configured;
      return true;
    }).map(function(p){
      var run = !p.configured ? "<span class='pill muted'>未配置</span>" : p.running ? "<span class='pill ok'>运行中</span>" : "<span class='pill muted'>已停止</span>";
      var duty = p.configured ? "<div class='range'><input type='range' min='0' max='100' value='" + p.duty + "' data-pwm-duty='" + p.id + "'" + (p.running ? "" : " disabled") + "><b data-pwm-val='" + p.id + "'>" + p.duty + "%</b></div>" : "<span class='muted'>—</span>";
      var ops = "";
      if (p.configured) {
        ops += "<button class='btn sm " + (p.running ? "ghost" : "primary") + "' data-pwm-run='" + p.id + "'>" + (p.running ? "停止" : "启动") + "</button>";
        ops += "<button class='link-btn' data-pwm-config='" + p.id + "'>配置</button>";
      } else {
        ops += "<button class='btn sm ghost' data-pwm-config='" + p.id + "'>配置</button>";
      }
      return "<tr><td><div class='name'>" + p.id + "</div><div class='sub'>" + (p.label ? esc(p.label) : "未命名") + "</div></td><td class='mono'>" + esc(p.freq) + "</td><td class='mono'>" + (p.bits === null ? "—" : p.bits + " bit") + "</td><td>" + run + "</td><td>" + duty + "</td><td class='acts'>" + ops + "</td></tr>";
    }).join("");
  }
  function renderPeriph(){
    $$("[data-periph]").forEach(function(b){ b.classList.toggle("active", b.getAttribute("data-periph") === state.periph); });
    renderPeriphSummary();
    var head = state.periph === "gpio" ? "<thead><tr><th>引脚</th><th>标签</th><th>方向</th><th>运行状态</th><th class='acts'>操作</th></tr></thead>" : "<thead><tr><th>资源</th><th>频率</th><th>分辨率</th><th>运行状态</th><th>占空比</th><th class='acts'>操作</th></tr></thead>";
    var rows = state.periph === "gpio" ? gpioRows() : pwmRows();
    if (!rows) rows = "<tr><td colspan='6'><div class='empty' style='border:0;background:transparent'><b>没有符合筛选的外设</b><p>切换筛选条件，或等待设备上报资源。</p></div></td></tr>";
    $("#periphBody").innerHTML = "<div class='table-wrap'><table class='tbl'>" + head + "<tbody>" + rows + "</tbody></table></div>";
  }

  function renderDma(){
    var counts = { all: DMA.length, free: 0, allocated: 0, disabled: 0 };
    DMA.forEach(function(d){ counts[d.state]++; });
    $("#dmaSum").innerHTML = "<span class='sum-item'><b>" + DMA.length + "</b>DMA 通道</span><span class='sum-item ok'><b>" + counts.allocated + "</b>已分配</span><span class='sum-item'><b>" + counts.free + "</b>空闲</span><span class='sum-item'><b>" + counts.disabled + "</b>已禁用</span>";
    var labels = { all: "全部", free: "空闲", allocated: "已分配", disabled: "已禁用" };
    $("#dmaChips").innerHTML = ["all", "free", "allocated", "disabled"].map(function(k){
      return "<button class='chip" + (state.dmaFilter === k ? " active" : "") + "' data-dma-filter='" + k + "'>" + labels[k] + "<span class='n'>" + counts[k] + "</span></button>";
    }).join("");
    var rows = DMA.filter(function(d){ return state.dmaFilter === "all" || d.state === state.dmaFilter; }).map(function(d){
      var st = d.state === "free" ? "<span class='pill blue'>空闲</span>" : d.state === "allocated" ? "<span class='pill ok'>已分配</span>" : "<span class='pill muted'>已禁用</span>";
      var bound = d.bound ? "<button class='link-btn' data-goto-res='" + d.bound + "'>" + d.bound + "</button>" : "<span class='muted'>未绑定</span>";
      var op = d.state === "allocated" ? "<button class='link-btn danger' data-dma-unbind='" + d.id + "'>解绑</button>" : d.state === "free" ? "<button class='link-btn' data-dma-bind='" + d.id + "'>去绑定</button>" : "<span class='muted'>—</span>";
      return "<tr><td><div class='name'>" + d.name + "</div><div class='sub mono'>CH" + d.id + "</div></td><td>" + esc(d.caps) + "</td><td class='mono'>" + d.burst + " B</td><td>" + st + "</td><td>" + bound + "</td><td class='muted'>" + esc(d.bus) + "</td><td class='acts'>" + op + "</td></tr>";
    }).join("");
    $("#dmaTable").innerHTML = "<table class='tbl'><thead><tr><th>通道</th><th>能力</th><th>最大突发</th><th>状态</th><th>绑定资源</th><th>可服务总线</th><th class='acts'>操作</th></tr></thead><tbody>" + rows + "</tbody></table>";
  }

  function statusPill(s){
    if (s === "online") return "<span class='pill ok'>在线</span>";
    if (s === "offline") return "<span class='pill muted'>离线</span>";
    if (s === "pending") return "<span class='pill warn'>等待中</span>";
    return "<span class='pill muted'>未知</span>";
  }
  function renderDevices(){
    var opts = "<option value=''>全部通道</option>";
    var seen = {};
    DEVICES.forEach(function(d){ if (!seen[d.channel]) { seen[d.channel] = 1; opts += "<option value='" + esc(d.channel) + "'>" + esc(d.channel) + "</option>"; } });
    $("#devChan").innerHTML = opts;
    $("#devChan").value = state.devChan;
    var list = DEVICES.filter(function(d){
      if (state.devQuery) {
        var q = state.devQuery.toLowerCase();
        if ((d.name + " " + d.type + " " + d.addr + " " + d.channel).toLowerCase().indexOf(q) < 0) return false;
      }
      if (state.devChan && d.channel !== state.devChan) return false;
      if (state.devStatus && d.status !== state.devStatus) return false;
      return true;
    });
    if (state.devView === "list") {
      var rows = list.map(function(d){
        var readings = d.readings.length ? "<div class='reading'>" + d.readings.map(function(x){ return "<span>" + esc(x) + "</span>"; }).join("") + "</div>" : "<span class='muted'>等待首条数据</span>";
        return "<tr><td><div class='dev-cell'><span class='dev-ic'><svg class='i i-sm'><use href='#i-plug'/></svg></span><div><div class='name'>" + esc(d.name) + "</div><div class='sub'>" + esc(d.type) + "</div></div></div></td><td><div>" + esc(d.channel) + "</div><div class='sub mono'>" + esc(d.res) + " · " + esc(d.addr) + "</div></td><td>" + readings + "</td><td>" + statusPill(d.status) + "</td><td class='muted'>" + esc(d.last) + "</td><td class='acts'><button class='link-btn' data-device='" + d.id + "'>查看</button></td></tr>";
      }).join("");
      if (!rows) rows = "<tr><td colspan='6'><div class='empty' style='border:0;background:transparent'><b>没有匹配的设备</b><p>调整搜索或筛选条件。</p></div></td></tr>";
      $("#devBody").innerHTML = "<div class='table-wrap'><table class='tbl'><thead><tr><th>设备</th><th>通道 / 地址</th><th>最新读数</th><th>状态</th><th>最后数据</th><th class='acts'>操作</th></tr></thead><tbody>" + rows + "</tbody></table></div><div class='detail-note'>共 " + list.length + " 台设备。列表只展示最近读数摘要，完整数据在设备详情中。</div>";
    } else {
      var cards = list.map(function(d){
        var readings = d.readings.length ? d.readings.map(function(x){ return "<span>" + esc(x) + "</span>"; }).join("") : "<span class='muted'>等待首条数据</span>";
        return "<div class='card' style='padding:14px;box-shadow:none'><div class='dev-cell'><span class='dev-ic'><svg class='i i-sm'><use href='#i-plug'/></svg></span><div style='flex:1'><div class='name'>" + esc(d.name) + "</div><div class='sub'>" + esc(d.type) + " · " + esc(d.addr) + "</div></div>" + statusPill(d.status) + "</div><div class='reading' style='margin-top:10px'>" + readings + "</div><div class='detail-note' style='padding-top:8px'>" + esc(d.channel) + " · " + esc(d.last) + "</div></div>";
      }).join("");
      $("#devBody").innerHTML = "<div style='display:grid;grid-template-columns:repeat(auto-fill,minmax(240px,1fr));gap:12px'>" + cards + "</div><div class='detail-note'>共 " + list.length + " 台设备。</div>";
    }
  }

  function otaPill(s){
    var map = { pending: ["等待中", "blue"], downloading: ["下载中", "blue"], verifying: ["校验中", "blue"], installing: ["安装中", "blue"], success: ["成功", "ok"], failed: ["失败", "bad"], timeout: ["超时", "bad"], needs_retry: ["需要重试", "warn"] };
    var m = map[s] || [s, "muted"];
    return "<span class='pill " + m[1] + "'>" + m[0] + "</span>";
  }
  function renderOta(){
    var groups = { all: OTA.length, running: 0, success: 0, failed: 0 };
    OTA.forEach(function(o){ if (o.status === "pending" || o.status === "downloading" || o.status === "verifying" || o.status === "installing") groups.running++; else if (o.status === "success") groups.success++; else groups.failed++; });
    var labels = { all: "全部", running: "进行中", success: "成功", failed: "失败" };
    $("#otaChips").innerHTML = ["all", "running", "success", "failed"].map(function(k){ return "<button class='chip" + (state.otaFilter === k ? " active" : "") + "' data-ota-filter='" + k + "'>" + labels[k] + "<span class='n'>" + groups[k] + "</span></button>"; }).join("");
    var startBtn = $("#otaStart");
    if (startBtn) { startBtn.disabled = groups.running > 0; startBtn.textContent = groups.running > 0 ? "升级进行中" : "发起升级"; }
    var list = OTA.filter(function(o){
      if (state.otaFilter === "all") return true;
      if (state.otaFilter === "running") return ["pending", "downloading", "verifying", "installing"].indexOf(o.status) >= 0;
      if (state.otaFilter === "success") return o.status === "success";
      return o.status !== "success" && ["pending", "downloading", "verifying", "installing"].indexOf(o.status) < 0;
    });
    var rows = list.map(function(o){
      var p = Math.max(0, Math.min(100, o.progress));
      var canCancel = o.status === "pending" || o.status === "downloading";
      var action = canCancel ? "<button class='link-btn danger' data-ota-cancel='" + o.id + "'>取消</button>" : "<span class='muted'>—</span>";
      var forced = o.forced ? "<span class='pill warn' style='margin-left:6px' title='" + esc(o.reason || "强制升级（未填写理由）") + "'>强制</span>" : "";
      var barColor = o.status === "success" ? "#22C55E" : (o.status === "failed" || o.status === "timeout") ? "#EF4444" : o.status === "needs_retry" ? "#F59E0B" : "#2E6BFF";
      return "<tr><td><div class='name'>" + esc(o.from) + " → " + esc(o.to) + "</div><div class='sub mono'>#" + o.id + "</div></td><td>" + otaPill(o.status) + forced + "</td><td><span class='progress'><i style='width:" + p + "%;background:" + barColor + "'></i></span><span class='mono'>" + p + "%</span></td><td class='mono'>" + esc(o.start) + "</td><td class='muted'>" + esc(o.end) + "</td><td class='acts'>" + action + "</td></tr>";
    }).join("");
    if (!rows) rows = "<tr><td colspan='6'><div class='empty' style='border:0;background:transparent'><b>没有匹配的升级记录</b><p>切换筛选条件。</p></div></td></tr>";
    $("#otaBody").innerHTML = "<div class='table-wrap'><table class='tbl'><thead><tr><th>版本</th><th>状态</th><th>进度</th><th>开始时间</th><th>耗时</th><th class='acts'>操作</th></tr></thead><tbody>" + rows + "</tbody></table></div><div class='detail-note'>取消只对等待中、下载中的任务开放；取消后的记录会落到失败并附带原因。</div>";
  }

  function renderLogs(){
    $$("#logTabs button").forEach(function(b){ b.classList.toggle("active", b.getAttribute("data-log") === state.logTab); });
    if (state.logTab === "realtime") {
      $("#logTools").innerHTML = "<span class='hint'>日志流</span><button class='switch on' data-action='log-stream' aria-label='日志流'></button><select class='select' id='logLevel'><option>ERROR</option><option>WARN</option><option selected>INFO</option><option>DEBUG</option><option>VERBOSE</option></select><button class='btn ghost' data-action='log-pause'><svg class='i i-sm'><use href='#i-pause'/></svg>" + (state.logPaused ? "继续" : "暂停滚动") + "</button><button class='btn ghost' data-action='log-clear'><svg class='i i-sm'><use href='#i-trash'/></svg>清屏</button><button class='btn ghost' data-action='log-export'><svg class='i i-sm'><use href='#i-download'/></svg>导出</button>";
      var lines = LOGS.slice().reverse().map(function(l){
        return "<div class='ln'><span class='ts'>" + l.ts + "</span><span class='lv " + l.lv + "'>" + l.lv + "</span><span class='tag'>" + l.tag + "</span><span class='msg" + (l.lv === "E" ? " err" : "") + "'>" + esc(l.msg) + "</span></div>";
      }).join("");
      $("#logBody").innerHTML = "<div style='padding:12px 14px'><div class='log-toolbar' style='padding:0 0 10px;border:0'><label class='search'><svg class='i i-sm'><use href='#i-search'/></svg><input id='logSearch' placeholder='过滤标签 / 消息 / 级别'></label><span class='hint'>已接收 1,284 行 · 上限 5,000</span><span class='spacer'></span><span class='hint'>贴底跟随 · 上滚后出现「回到底部」</span></div><div class='term' id='logTerm'>" + lines + "</div><div class='detail-note'>运行时间是固件内部运行时长，不是墙钟时间；暂停滚动不会停止接收。</div></div>";
      var term = $("#logTerm");
      term.scrollTop = term.scrollHeight;
      var search = $("#logSearch");
      search.addEventListener("input", function(){
        var q = search.value.toLowerCase();
        $$(".ln", term).forEach(function(ln){ ln.style.display = !q || ln.textContent.toLowerCase().indexOf(q) >= 0 ? "" : "none"; });
      });
    } else {
      $("#logTools").innerHTML = "<button class='btn ghost' data-action='log-export'><svg class='i i-sm'><use href='#i-download'/></svg>导出当前结果</button><button class='btn ghost' data-action='log-clean'><svg class='i i-sm'><use href='#i-trash'/></svg>清理</button>";
      var rows = LOGS.slice(0, 6).map(function(l){
        return "<tr><td class='mono'>2026/11/19 " + l.ts + "</td><td><span class='pill " + (l.lv === "E" ? "bad" : l.lv === "W" ? "warn" : "muted") + "'>" + l.lv + "</span></td><td class='mono'>" + l.tag + "</td><td>" + esc(l.msg) + "</td></tr>";
      }).join("");
      $("#logBody").innerHTML = "<div style='padding:12px 14px'><div class='log-toolbar' style='padding:0 0 12px;border:0'><select class='select'><option>最近 1 小时</option><option>最近 24 小时</option><option>自定义</option></select><select class='select'><option>全部级别</option><option>ERROR</option><option>WARN</option><option>INFO</option></select><input class='select' style='width:120px' placeholder='Tag'><label class='search'><svg class='i i-sm'><use href='#i-search'/></svg><input placeholder='关键词，回车查询'></label><button class='btn primary sm'>查询</button></div><div class='table-wrap'><table class='tbl'><thead><tr><th>时间</th><th>级别</th><th>Tag</th><th>消息</th></tr></thead><tbody>" + rows + "</tbody></table></div><div class='detail-note'>共 1,284 条 · 第 1 / 13 页 · 每页 100 条</div></div>";
    }
  }

  function renderTerm(){
    var groups = [["UART", ["UART0-101", "UART1-103"]], ["SPI", ["SPI2-105"]]];
    var html = "";
    groups.forEach(function(g){
      html += "<div class='chan-group'>" + g[0] + "</div>";
      g[1].forEach(function(key){
        var parts = key.split("-");
        var res = findRes(busKeyOf(parts[0]), parts[0]);
        var ch = null;
        if (res) res.channels.forEach(function(c){ if (String(c.id) === parts[1]) ch = c; });
        if (!ch) return;
        html += "<button class='chan-item" + (state.term === key ? " active" : "") + "' data-term='" + key + "'><b>" + esc(ch.name) + "</b><span>" + parts[0] + " · " + esc(ch.params) + "</span></button>";
      });
    });
    $("#chanRail").innerHTML = html;
    var parts = state.term.split("-");
    var res = findRes(busKeyOf(parts[0]), parts[0]);
    var ch = null;
    if (res) res.channels.forEach(function(c){ if (String(c.id) === parts[1]) ch = c; });
    $("#termHead").innerHTML = ch ? "<b>" + esc(ch.name) + "</b><span class='tag'>" + parts[0] + "</span><span class='pill ok'>正常</span><span class='hint'>" + esc(ch.params) + " · 已选通道</span><span class='spacer'></span><button class='btn ghost' data-action='term-pause'><svg class='i i-sm'><use href='#i-pause'/></svg>暂停</button><button class='btn ghost' data-action='term-clear'><svg class='i i-sm'><use href='#i-trash'/></svg>清空</button><button class='btn ghost' data-action='term-export'><svg class='i i-sm'><use href='#i-download'/></svg>导出</button>" : "<span class='hint'>从左侧选择通道开始</span>";
    var lines = (TERM[state.term] || []).map(function(l){
      return "<div class='txrx " + l.dir + "'><span class='t'>" + l.t + "</span><span class='d'>" + l.dir.toUpperCase() + "</span><span class='b'>" + esc(l.data) + "</span>" + (l.err ? "<span class='err'>[" + esc(l.err) + "]</span>" : "") + "</div>";
    }).join("");
    $("#termLog").innerHTML = lines;
    var tl = $("#termLog"); tl.scrollTop = tl.scrollHeight;
  }

  function renderTab(tab){
    if (tab === "bus") renderBus();
    if (tab === "periph") renderPeriph();
    if (tab === "dma") renderDma();
    if (tab === "devices") renderDevices();
    if (tab === "ota") renderOta();
    if (tab === "logs") renderLogs();
    if (tab === "terminal") renderTerm();
  }
  function switchTab(tab){
    state.tab = tab;
    $$(".tab").forEach(function(t){ t.classList.toggle("active", t.getAttribute("data-tab") === tab); t.setAttribute("aria-selected", t.getAttribute("data-tab") === tab ? "true" : "false"); });
    $$(".pane").forEach(function(p){ p.classList.toggle("active", p.getAttribute("data-pane") === tab); });
    renderTab(tab);
  }

  function openGpio(pin, g){
    var v = g || { pin: pin, label: "", dir: "输出", level: 0 };
    var body = "<div class='field'><label>引脚</label><input value='GPIO" + pin + "' disabled></div>" +
      "<div class='grid2'><div class='field'><label>方向</label><select id='gDir'><option" + (v.dir.indexOf("输出") === 0 ? " selected" : "") + ">输出</option><option" + (v.dir.indexOf("输入（上拉") === 0 ? " selected" : "") + ">输入（上拉）</option><option>输入（下拉）</option><option>输入</option></select></div>" +
      "<div class='field'><label>初始电平</label><select id='gLevel'><option value='0'>低电平</option><option value='1'" + (v.level ? " selected" : "") + ">高电平</option></select></div></div>" +
      "<div class='field'><label>标签</label><input id='gLabel' placeholder='如：继电器 1' value='" + esc(v.label) + "'></div>" +
      "<div class='form-note'>保存后会写入外设配置并下发；引脚已被总线占用时不可配置。</div>";
    openDrawer({ title: "配置 GPIO" + pin, sub: "引脚直控 · 不走通道", body: body, submit: "保存并下发", onSubmit: function(){
      var dir = $("#gDir").value; var level = Number($("#gLevel").value); var label = $("#gLabel").value.trim();
      var item = GPIO.filter(function(x){ return x.pin === pin; })[0];
      if (!item) { item = { pin: pin, label: "", dir: "", level: null, configured: false, owner: "" }; GPIO.push(item); }
      item.dir = dir; item.label = label; item.configured = true; item.level = dir.indexOf("输出") === 0 ? level : 0;
      closeDrawer(); renderPeriph(); toast("配置已保存并下发", true);
    }});
  }
  function openPwm(id){
    var p = PWM.filter(function(x){ return x.id === id; })[0] || { id: id, label: "", freq: "5 kHz", bits: 12, running: false, duty: 0, configured: false };
    var body = "<div class='field'><label>资源</label><input value='" + id + "' disabled></div>" +
      "<div class='grid2'><div class='field'><label>频率</label><select id='pFreq'><option>1 kHz</option><option" + (p.freq === "5 kHz" ? " selected" : "") + ">5 kHz</option><option" + (p.freq === "25 kHz" ? " selected" : "") + ">25 kHz</option></select></div>" +
      "<div class='field'><label>分辨率</label><select id='pBits'><option>8 bit</option><option>10 bit</option><option" + (p.bits === 12 ? " selected" : "") + ">12 bit</option></select></div></div>" +
      "<div class='field'><label>标签</label><input id='pLabel' placeholder='如：LED 调光' value='" + esc(p.label) + "'></div>" +
      "<div class='field'><label>自动启动</label><select id='pAuto'><option value='0'>否</option><option value='1'>是</option></select></div>" +
      "<div class='form-note'>PWM 由 LEDC 直控；运行中修改频率会短暂中断输出。</div>";
    openDrawer({ title: "配置 " + id, sub: "PWM 直控 · 不走通道", body: body, submit: "保存并下发", onSubmit: function(){
      p.freq = $("#pFreq").value; p.bits = parseInt($("#pBits").value, 10); p.label = $("#pLabel").value.trim(); p.configured = true;
      if ($("#pAuto").value === "1") p.running = true;
      closeDrawer(); renderPeriph(); toast("配置已保存并下发", true);
    }});
  }
  function openDevice(){
    var body = "<div class='field'><label>设备型号</label><select id='dType'><option>电表 SN3001</option><option>BMS 嘉佰达</option><option>温湿度 LKTH01</option><option>逆变器 Techfine</option></select></div>" +
      "<div class='field'><label>通道</label><select id='dChan'><option>RS485 采集（UART0）</option><option>逆变器（UART1）</option><option>温湿度（SPI2）</option></select></div>" +
      "<div class='grid2'><div class='field'><label>设备名称</label><input id='dName' placeholder='如：1 号电表'></div><div class='field'><label>地址</label><input id='dAddr' placeholder='如：3'></div></div>" +
      "<div class='field'><label>采集间隔</label><select id='dInt'><option>1000 ms</option><option selected>5000 ms</option><option>10000 ms</option></select></div>" +
      "<div class='form-note'>创建后会立即下发初始化序列；型号决定解析方式与可用通道。</div>";
    openDrawer({ title: "快速创建设备", sub: "在当前节点上直接挂载", body: body, submit: "创建并下发", onSubmit: function(){
      closeDrawer(); toast("设备已创建，初始化指令已下发", true);
    }});
  }
  function openOta(){
    var body = "<div class='field'><label>目标固件</label><select id='oFw'><option>2.9.0（2026/11/19）</option><option>2.8.0（当前版本）</option></select></div>" +
      "<div class='form-note'>仅展示与该节点型号兼容的固件；不兼容的版本会置灰并说明原因。升级中设备会重启，页面可能短暂失联。</div>";
    openDrawer({ title: "OTA 升级", sub: "当前版本 2.8.0", body: body, submit: "开始升级", onSubmit: function(){
      closeDrawer(); switchTab("ota"); toast("升级任务已创建", true);
    }});
  }
  function sendTerm(){
    var input = $("#termInput");
    var val = input.value.trim();
    if (!val) { toast("请输入要发送的数据"); return; }
    if (state.hex === "hex") {
      var clean = val.replace(/ /g, "");
      if (!/^[0-9a-fA-F]+$/.test(clean) || clean.length % 2 !== 0) { toast("无效的 HEX 数据（需偶数长度）"); return; }
    }
    var key = state.term;
    if (!TERM[key]) TERM[key] = [];
    var now = new Date();
    var ts = String(now.getHours()).padStart(2, "0") + ":" + String(now.getMinutes()).padStart(2, "0") + ":" + String(now.getSeconds()).padStart(2, "0") + ".000";
    var bytes = state.hex === "hex" ? val.replace(/ /g, "").length / 2 : val.length;
    TERM[key].push({ dir: "tx", t: ts, n: bytes, data: state.hex === "hex" ? val.toUpperCase() : val });
    input.value = "";
    renderTerm();
    toast("已发送 " + bytes + " 字节", true);
    setTimeout(function(){
      TERM[key].push({ dir: "rx", t: ts, n: bytes, data: state.hex === "hex" ? "01 03 02 00 34 B5 33" : "OK" });
      if (state.tab === "terminal") renderTerm();
    }, 420);
  }

  document.addEventListener("click", function(e){
    var t = e.target.closest ? e.target.closest("[data-tab],[data-bus],[data-res],[data-action],[data-periph],[data-dma-filter],[data-ota-filter],[data-view],[data-log],[data-term],[data-gpio-toggle],[data-gpio-read],[data-gpio-config],[data-pwm-run],[data-pwm-config],[data-edit-channel],[data-del-channel],[data-fill-addr],[data-goto-res],[data-dma-unbind],[data-dma-bind],[data-ota-cancel],[data-device],[data-hex]") : null;
    if (!t) return;
    var a = t.getAttribute("data-action");
    if (t.hasAttribute("data-tab")) return switchTab(t.getAttribute("data-tab"));
    if (t.hasAttribute("data-bus")) { state.bus = t.getAttribute("data-bus"); state.res = BUS[state.bus].resources[0].id; state.scan = null; renderBus(); return; }
    if (t.hasAttribute("data-res")) { state.res = t.getAttribute("data-res"); state.scan = null; renderResList(); renderDetail(); return; }
    if (t.hasAttribute("data-periph")) { state.periph = t.getAttribute("data-periph"); renderPeriph(); return; }
    if (t.hasAttribute("data-dma-filter")) { state.dmaFilter = t.getAttribute("data-dma-filter"); renderDma(); return; }
    if (t.hasAttribute("data-ota-filter")) { state.otaFilter = t.getAttribute("data-ota-filter"); renderOta(); return; }
    if (t.hasAttribute("data-view")) { state.devView = t.getAttribute("data-view"); $$("#devView button").forEach(function(b){ b.classList.toggle("active", b.getAttribute("data-view") === state.devView); }); renderDevices(); return; }
    if (t.hasAttribute("data-log")) { state.logTab = t.getAttribute("data-log"); renderLogs(); return; }
    if (t.hasAttribute("data-term")) { state.term = t.getAttribute("data-term"); renderTerm(); return; }
    if (t.hasAttribute("data-hex")) { state.hex = t.getAttribute("data-hex"); $$("#hexMode button").forEach(function(b){ b.classList.toggle("active", b.getAttribute("data-hex") === state.hex); }); return; }
    if (t.hasAttribute("data-gpio-toggle")) { var g = GPIO.filter(function(x){ return x.pin === Number(t.getAttribute("data-gpio-toggle")); })[0]; if (g) { g.level = g.level ? 0 : 1; renderPeriph(); toast("已写入 " + (g.level ? "高" : "低") + "电平，等待设备回执", true); } return; }
    if (t.hasAttribute("data-gpio-read")) { toast("已读取 GPIO" + t.getAttribute("data-gpio-read") + "，等待设备回执"); return; }
    if (t.hasAttribute("data-gpio-config")) { var pin = Number(t.getAttribute("data-gpio-config")); var gp = GPIO.filter(function(x){ return x.pin === pin; })[0]; openGpio(pin, gp); return; }
    if (t.hasAttribute("data-pwm-run")) { var pid = t.getAttribute("data-pwm-run"); var p = PWM.filter(function(x){ return x.id === pid; })[0]; if (p) { p.running = !p.running; renderPeriph(); toast(p.running ? "PWM 已启动" : "PWM 已停止", true); } return; }
    if (t.hasAttribute("data-pwm-config")) { openPwm(t.getAttribute("data-pwm-config")); return; }
    if (t.hasAttribute("data-edit-channel")) { var cid = t.getAttribute("data-edit-channel"); var rr = currentRes(); var cc = rr.channels.filter(function(c){ return String(c.id) === cid; })[0]; if (cc) channelForm(rr, cc); return; }
    if (t.hasAttribute("data-del-channel")) { var did = t.getAttribute("data-del-channel"); var r2 = currentRes(); var c2 = r2.channels.filter(function(c){ return String(c.id) === did; })[0]; if (!c2) return; askConfirm("删除通道", "删除「" + c2.name + "」会向节点下发配置变更，该通道上的设备将停止采集。此操作不可恢复。", "删除", function(){ r2.channels = r2.channels.filter(function(c){ return String(c.id) !== did; }); renderBus(); toast("通道已删除", true); }); return; }
    if (t.hasAttribute("data-fill-addr")) { var addr = t.getAttribute("data-fill-addr"); state.scan = null; renderDetail(); channelForm(currentRes(), null); setTimeout(function(){ var el = $("#fAddr"); if (el) el.value = addr; }, 30); return; }
    if (t.hasAttribute("data-goto-res")) { var rid = t.getAttribute("data-goto-res"); var found = null; Object.keys(BUS).forEach(function(k){ BUS[k].resources.forEach(function(r){ if (r.id === rid) { found = k; state.res = rid; } }); }); if (found) { state.bus = found; state.scan = null; switchTab("bus"); } return; }
    if (t.hasAttribute("data-dma-unbind")) { var d = dmaOf(Number(t.getAttribute("data-dma-unbind"))); if (!d) return; askConfirm("解绑 DMA", "解绑后该资源将回退到中断或轮询模式，吞吐会下降，但采集不会中断。", "解绑", function(){ d.state = "free"; d.bound = ""; allResources().forEach(function(r){ if (r.dma === d.id) r.dma = null; }); renderDma(); renderBus(); toast("DMA 已解绑", true); }); return; }
    if (t.hasAttribute("data-dma-bind")) { switchTab("bus"); toast("请在资源详情中选择 DMA"); return; }
    if (t.hasAttribute("data-ota-cancel")) { var oid = Number(t.getAttribute("data-ota-cancel")); askConfirm("取消升级", "取消会中断设备侧的升级流程，设备仍运行当前版本。", "取消升级", function(){ var rec = OTA.filter(function(o){ return o.id === oid; })[0]; if (rec) { rec.status = "failed"; rec.end = "已取消"; } renderOta(); toast("已取消升级", true); }); return; }
    if (t.hasAttribute("data-device")) { toast("打开边缘设备详情（原型不跳转）"); return; }
    if (a === "refresh") { toast("已刷新节点数据", true); return; }
    if (a === "refresh-periph") { renderPeriph(); toast("外设状态已刷新", true); return; }
    if (a === "refresh-dma") { renderDma(); toast("DMA 状态已刷新", true); return; }
    if (a === "refresh-devices") { renderDevices(); toast("设备列表已刷新", true); return; }
    if (a === "refresh-ota") { renderOta(); toast("升级记录已刷新", true); return; }
    if (a === "query-res") { toast("已向节点下发资源查询，等待上报", true); return; }
    if (a === "new-channel") { channelForm(currentRes(), null); return; }
    if (a === "goto-devices") { switchTab("devices"); return; }
    if (a === "scan") { state.scan = ["0x76", "0x77"]; renderDetail(); toast("扫描完成，发现 2 个地址", true); return; }
    if (a === "create-device") { openDevice(); return; }
    if (a === "ota") { openOta(); return; }
    if (a === "close-drawer") { closeDrawer(); return; }
    if (a === "confirm-cancel") { closeConfirm(); return; }
    if (a === "log-stream") { t.classList.toggle("on"); toast(t.classList.contains("on") ? "日志流已开启" : "日志流已关闭", true); return; }
    if (a === "log-pause") { state.logPaused = !state.logPaused; renderLogs(); toast(state.logPaused ? "已暂停滚动，日志继续接收" : "已恢复滚动"); return; }
    if (a === "log-clear") { $("#logTerm").innerHTML = ""; toast("已清屏（仅清空本地显示）", true); return; }
    if (a === "log-export") { toast("已导出当前过滤结果"); return; }
    if (a === "log-clean") { askConfirm("清理历史日志", "将删除该节点的历史日志记录，此操作不可恢复。", "清理", function(){ toast("已清理历史日志", true); }); return; }
    if (a === "term-pause") { toast("已暂停终端刷新"); return; }
    if (a === "term-clear") { $("#termLog").innerHTML = ""; return; }
    if (a === "term-export") { toast("已导出终端记录"); return; }
    if (a === "send-term") { sendTerm(); return; }
  });

  $("#drawerMask").addEventListener("click", closeDrawer);
  $("#confirmMask").addEventListener("click", closeConfirm);
  $("#confirmOk").addEventListener("click", function(){ var cb = confirmCallback; closeConfirm(); if (cb) cb(); });
  $("#drawerSubmit").addEventListener("click", function(){ if (currentSubmit) currentSubmit(); });
  $("#resSearch").addEventListener("input", function(e){ state.resQuery = e.target.value; renderResList(); });
  $("#periphFilter").addEventListener("change", function(e){ state.periphFilter = e.target.value; renderPeriph(); });
  $("#devSearch").addEventListener("input", function(e){ state.devQuery = e.target.value; renderDevices(); });
  $("#devChan").addEventListener("change", function(e){ state.devChan = e.target.value; renderDevices(); });
  $("#devStatus").addEventListener("change", function(e){ state.devStatus = e.target.value; renderDevices(); });
  $("#termInput").addEventListener("keydown", function(e){ if (e.key === "Enter") sendTerm(); });
  document.addEventListener("input", function(e){
    if (e.target.hasAttribute && e.target.hasAttribute("data-pwm-duty")) {
      var id = e.target.getAttribute("data-pwm-duty");
      var p = PWM.filter(function(x){ return x.id === id; })[0];
      if (p) { p.duty = Number(e.target.value); var lab = $("[data-pwm-val='" + id + "']"); if (lab) lab.textContent = p.duty + "%"; }
    }
  });
  document.addEventListener("change", function(e){
    if (e.target.hasAttribute && e.target.hasAttribute("data-pwm-duty")) {
      toast("占空比已下发（" + e.target.getAttribute("data-pwm-duty") + "）", true);
    }
  });

  renderBus();
  switchTab("bus");
})();