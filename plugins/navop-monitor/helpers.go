package main

import (
	"sync"
	"time"
)

type metricIn struct {
	Name      string  `json:"name"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
}

// 有状态指标历史（§400）：插件进程长驻（plugindriver cmd.Start +
// readLoop），ring 在进程内存累积——宿主重启或插件重载即清空（会话级
// 历史，与 cmpsync 的"勾选集不持久化"同纪律）。
type sample struct {
	At     string
	Metric string
	Value  string
	Group  string
	Status string
}

const ringCap = 120 // 120 个采集点 × 指标数，足够画一段曲线

var history = struct {
	sync.Mutex
	points []sample
}{}

// appendSample 追加一次采集的指标集（超过 ringCap 丢弃最旧的）。
func appendSample(metrics []sample) {
	history.Lock()
	defer history.Unlock()
	history.points = append(history.points, metrics...)
	if len(history.points) > ringCap*8 {
		history.points = append([]sample(nil), history.points[len(history.points)-ringCap*8:]...)
	}
}

// snapshot 返回历史副本（按 metric 过滤；空=全部）。
func snapshot(metric string) []sample {
	history.Lock()
	defer history.Unlock()
	out := []sample{}
	for _, p := range history.points {
		if metric == "" || p.Metric == metric {
			out = append(out, p)
		}
	}
	return out
}

func nowStamp() string { return time.Now().UTC().Format("15:04:05") }
