// EHomeSystem 边缘设备高频数据交互压测器
// 模拟 N 个节点, 每节点独立 MQTT 连接, 按指定速率上报 DataReport (0x03) 帧。
// 帧字段: f1=channelID f2=timestamp(ms) f3=sequence f4=raw(4B) f5=errorCode=0 f7=edgeDeviceID
// 用法:
//   go build -o ehload .
//   ./ehload --nodes 500 --rate 4 --duration 60 --qos 1 --prefix SIM
//   总速率 = nodes × rate (msg/s)
package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// --- frame 编码 (与 pkg/frame 兼容) ---

func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func fieldTag(field, wire uint8) byte { return field<<3 | wire }

func dataReportFrame(channelID, ts, seq, edgeDeviceID uint64, raw []byte) []byte {
	b := []byte{0x03}
	b = append(b, fieldTag(1, 0))
	b = appendVarint(b, channelID)
	b = append(b, fieldTag(2, 0))
	b = appendVarint(b, ts)
	b = append(b, fieldTag(3, 0))
	b = appendVarint(b, seq)
	b = append(b, fieldTag(4, 2))
	b = appendVarint(b, uint64(len(raw)))
	b = append(b, raw...)
	b = append(b, fieldTag(5, 0))
	b = appendVarint(b, 0) // errorCode=0
	b = append(b, fieldTag(7, 0))
	b = appendVarint(b, edgeDeviceID)
	return b
}

// --- 统计 ---

var (
	sentTotal   atomic.Uint64
	failTotal   atomic.Uint64
	latencySum  atomic.Uint64 // us
	latencyN    atomic.Uint64
	latencyMax  atomic.Uint64 // us
	activeNodes atomic.Int64
)

// raw 数据: lk_th01 [temp_hi temp_lo hum_hi hum_lo] => temp=25.0 hum=40.0
func makeRaw() []byte {
	return []byte{0x00, 0xFA, 0x01, 0x90}
}

func nodeWorker(wg *sync.WaitGroup, broker string, nodeID string, edgeDeviceID uint64, interval time.Duration, qos byte, dur time.Duration, seed int64) {
	defer wg.Done()
	rng := rand.New(rand.NewSource(seed))

	opts := mqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID(nodeID).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(500 * time.Millisecond).
		SetKeepAlive(60 * time.Second).
		SetOrderMatters(false).
		SetConnectTimeout(5 * time.Second)
	client := mqtt.NewClient(opts)
	tok := client.Connect()
	if !tok.WaitTimeout(10 * time.Second) || tok.Error() != nil {
		log.Printf("[%s] connect failed: %v", nodeID, tok.Error())
		return
	}
	defer client.Disconnect(100)

	activeNodes.Add(1)
	defer activeNodes.Add(-1)

	topic := "nodes/" + nodeID + "/up"
	var seq uint64
	start := time.Now()
	deadline := start.Add(dur)
	// 微抖动避免所有节点同时发 (真实设备时钟不同步)
	time.Sleep(time.Duration(rng.Int63n(int64(interval))))

	for time.Now().Before(deadline) {
		seq++
		ts := uint64(time.Now().UnixMilli())
		frame := dataReportFrame(1, ts, seq, edgeDeviceID, makeRaw())
		t0 := time.Now()
		t := client.Publish(topic, qos, false, frame)
		// QoS1 等待 ack; QoS0 不等待 (只记录发送耗时)
		if qos > 0 {
			if !t.WaitTimeout(5 * time.Second) {
				failTotal.Add(1)
			} else if t.Error() != nil {
				failTotal.Add(1)
			} else {
				lat := time.Since(t0).Microseconds()
				latencySum.Add(uint64(lat))
				latencyN.Add(1)
				for {
					cur := latencyMax.Load()
					if uint64(lat) <= cur || latencyMax.CompareAndSwap(cur, uint64(lat)) {
						break
					}
				}
			}
		} else {
			lat := time.Since(t0).Microseconds()
			latencySum.Add(uint64(lat))
			latencyN.Add(1)
			for {
				cur := latencyMax.Load()
				if uint64(lat) <= cur || latencyMax.CompareAndSwap(cur, uint64(lat)) {
					break
				}
			}
		}
		sentTotal.Add(1)
		time.Sleep(interval)
	}
}

func main() {
	var (
		nodes      int
		rate       float64
		duration   int
		qos        int
		prefix     string
		deviceBase int
		broker     string
	)
	flag.IntVar(&nodes, "nodes", 100, "模拟节点数")
	flag.Float64Var(&rate, "rate", 2, "每节点每秒上报次数")
	flag.IntVar(&duration, "duration", 60, "压测时长(秒)")
	flag.IntVar(&qos, "qos", 1, "MQTT QoS")
	flag.StringVar(&prefix, "prefix", "SIM", "节点名前缀")
	flag.IntVar(&deviceBase, "device-base", 1, "edge_device_id 起始值 (用于把 SIM0001 映射到具体设备 id)")
	flag.StringVar(&broker, "broker", "tcp://127.0.0.1:1883", "MQTT broker")
	flag.Parse()

	if nodes <= 0 || rate <= 0 {
		log.Fatal("nodes/rate 必须为正")
	}
	interval := time.Duration(float64(time.Second) / rate)
	totalRate := float64(nodes) * rate
	log.Printf("压测开始: nodes=%d rate=%.1fHz/节点 总速率=%.0f msg/s qos=%d duration=%ds broker=%s",
		nodes, rate, totalRate, qos, duration, broker)

	var wg sync.WaitGroup
	for i := 1; i <= nodes; i++ {
		nodeID := fmt.Sprintf("%s%04d", prefix, i)
		devID := uint64(deviceBase + i - 1)
		wg.Add(1)
		go nodeWorker(&wg, broker, nodeID, devID, interval, byte(qos), time.Duration(duration)*time.Second, int64(i)*7919)
	}

	// 每秒打印进度
	lastSent := uint64(0)
	tick := time.NewTicker(1 * time.Second)
	defer tick.Stop()
	reportDone := make(chan struct{})
	go func() {
		for {
			select {
			case <-reportDone:
				return
			case <-tick.C:
				s := sentTotal.Load()
				f := failTotal.Load()
				avg := uint64(0)
				if n := latencyN.Load(); n > 0 {
					avg = latencySum.Load() / n
				}
				fmt.Printf("[t] sent=%d (+%d/s) fail=%d active_nodes=%d pub_lat_avg=%dus max=%dus\n",
					s, s-lastSent, f, activeNodes.Load(), avg, latencyMax.Load())
				lastSent = s
			}
		}
	}()

	wg.Wait()
	close(reportDone)
	s := sentTotal.Load()
	f := failTotal.Load()
	avg := uint64(0)
	if n := latencyN.Load(); n > 0 {
		avg = latencySum.Load() / n
	}
	fmt.Printf("=== 压测结束 === sent=%d fail=%d 实际速率=%.1f msg/s pub_lat_avg=%dus max=%dus\n",
		s, f, float64(s)/float64(duration), avg, latencyMax.Load())
	if f > 0 {
		fmt.Fprintf(os.Stderr, "WARN: %d 条发布失败\n", f)
	}
}

