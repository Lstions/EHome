OTA 升级验证最终报告                                                                                                                                     
测试环境                                                                                                                                                 
- HTTP 服务器: 10.42.0.1:8899 (正常运行)                                                                                                                 
- 设备 IP: 10.42.0.155:8088                                                                                                                              
- 固件: ehome_collector.bin (1,064,464 bytes)                                                                                                            
- SHA256: dbf947cba212dcc43ea39123188eb441323b4998f57c19ce46644c808bfb9d59                                                                               
测试结果                                                                                                                                                 
✅ 成功的步骤                                                                                                                                            
1. HTTP 服务器 - 正常提供固件下载                                                                                                                        
2. OTA 命令发送 - 通过 TCP 成功发送 (121 bytes)                                                                                                          
3. 设备接收命令 - 日志确认收到 OTA_CMD                                                                                                                   
4. 固件下载 - 完整下载 0% → 100% (约 10 秒)                                                                                                              
5. SHA256 验证启动 - 开始计算 ota_1 分区校验和                                                                                                           
❌ 失败的步骤                                                                                                                                            
6. OTA 完成重启 - 设备卡住，未执行 esp_restart()                                                                                                         
7. 新固件验证 - 设备继续运行旧固件 25+ 分钟                                                                                                              
问题分析                                                                                                                                                 
                                                                                                                                                         
从设备日志观察，OTA 进程在以下阶段卡住：                                                                                                                 
                                                                                                                                                         
I (185213) OTA: OTA image written, validating checksum...                                                                                                
I (185214) OTA: Computing SHA256 of 'ota_1' (offset 0x1d0000, 1064464 bytes)                                                                             
                                                                                                                                                         
之后再无 OTA 相关日志输出。根据 ota.c 代码 (第 480-540 行)，成功流程应该是：                                                                             
                                                                                                                                                         
1. ✅ 下载完成                                                                                                                                           
2. ✅ NVS 状态更新为 VERIFYING (2)                                                                                                                       
3. ✅ 开始 SHA256 计算                                                                                                                                   
4. ❓ 卡在此处 - 未见到以下日志：                                                                                                                        
   - "Boot partition set to 'ota_1'"                                                                                                                     
   - "OTA success, restarting in 1s..."                                                                                                                  
5. ❌ esp_restart() 未执行                                                                                                                               
可能的原因                                                                                                                                               
                                                                                                                                                         
1. SHA256 计算卡死 - 读取 1MB 分区数据时可能遇到 Flash 读取问题                                                                                          
2. esp_ota_set_boot_partition() 失败 - 分区切换 API 可能返回错误                                                                                         
3. FreeRTOS 任务死锁 - OTA 任务可能在等待某个资源                                                                                                        
4. 内存不足 - 计算 SHA256 时堆内存耗尽                                                                                                                   
已尝试的解决方法                                                                                                                                         
                                                                                                                                                         
1. 发送重启命令 (0x0E) - 设备无此命令处理器                                                                                                              
2. 发送 Factory Reset (0x06, channel 0) - 未触发重启                                                                                                     
3. 发送多种消息类型 - 设备仍在运行旧固件                                                                                                                 
4. 防火墙规则添加 - 确认网络连通性正常                                                                                                                   
建议的修复方案                                                                                                                                           
                                                                                                                                                         
1. 检查 SHA256 计算代码 - 查看 components/ota/ota.c 中的 mbedtls_sha256_starts/updates/finish 调用                                                       
2. 添加错误日志 - 在 esp_ota_set_boot_partition() 调用前后添加详细日志                                                                                   
3. 检查分区表 - 确认 ota_1 分区 (0x1d0000) 大小足够容纳固件                                                                                              
4. 验证 Flash 读取 - 使用 esp_partition_read() 测试分区可读性                                                                                            
5. 添加看门狗 - 如果 SHA256 计算超过 30 秒，触发重启                                                                                                     
当前状态                                                                                                                                                 
                                                                                                                                                         
- HTTP 服务器: 运行中 (proc_9264ece42a3f)                                                                                                                
- 设备: 在线运行旧固件                                                                                                                                   
- 监控进程: 已启动 (proc_ec3f27a3883b)                                                                                                                   
                                                                                                                                                         
结论: OTA 下载功能正常，但固件验证和重启机制存在问题，需要进一步调试 SHA256 计算和分区切换逻辑。    