-- 集成复验种子：还原 P1/P2/P4 各自需要的真实形态数据
-- P1 需要：设备 hardware_id='1'(Modbus地址) 且挂在 UART0 通道上
INSERT INTO nodes (node_id,name,status,protocol_version,capabilities,firmware_version,created_at,updated_at)
VALUES ('F0F5BDFFFE02','测试C6N8','online','2.6','{}','2.5.21',now(),now());

INSERT INTO channels (node_id,hardware_type,hardware_id,bus_type,bus_config,enabled,created_at,updated_at)
VALUES ('F0F5BDFFFE02','UART','UART0','UART','101100002580',true,now(),now());

-- P1: 设备级 hardware_id='1' 是 Modbus 地址，通道级 hardware_id='UART0' 才是总线
INSERT INTO edge_devices (name,node_id,channel_id,type,hardware_id,interval_ms,enabled,status,created_at,updated_at)
VALUES ('测试雨量计','F0F5BDFFFE02',1,'sn3001_rain','1',5000,true,'active',now(),now()),
       ('测试BMS','F0F5BDFFFE02',1,'jiabaida_bms','',5000,true,'active',now(),now());

-- P2: 真实形状的 data_json（含 sensors 与 raw_hex）
INSERT INTO device_data (device_id,node_id,data_json,timestamp,created_at,edge_device_id,logical_device_id)
VALUES
 (1,'F0F5BDFFFE02','{"channel_id":1,"raw_hex":"01030200057847","sensors":[{"Name":"rainfall","Value":0.5,"Unit":"mm","StringValue":""}],"timestamp":1}', now(), now(), 2, 1),
 (1,'F0F5BDFFFE02','{"channel_id":1,"raw_hex":"0103020000b844","sensors":[{"Name":"rainfall","Value":0,"Unit":"mm","StringValue":""}],"timestamp":2}', now(), now(), 2, 1);

-- P4: 固件记录（OTA 弹层选中后才有"固件信息"区，含 64 字符 MD5）
INSERT INTO firmwares (version,checksum,size_bytes,url,filename,storage_path,changelog,created_at)
VALUES ('2.5.21','b186ea677564e22834ea6254a75b4e2a0e696ae0066c7119d17689a96d736295',1413760,
        'http://127.0.0.1:18094/fw.bin','ehome_collector.bin','firmwares/ehome_collector.bin',
        '修复配置同步假绿；新增写入侧容量门禁',now());
