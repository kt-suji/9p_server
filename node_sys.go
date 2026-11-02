package main

import (
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"
)

var (
	serverCPUMu      sync.RWMutex
	serverCPUPercent float64 = 5.0
)

func init() {
	// サーバーのCPU使用率を1秒周期で常時バックグラウンドサンプリング
	go func() {
		for {
			p, err := cpu.Percent(time.Second, false)
			if err == nil && len(p) > 0 {
				serverCPUMu.Lock()
				serverCPUPercent = p[0]
				serverCPUMu.Unlock()
			} else {
				time.Sleep(time.Second)
			}
		}
	}()

	RegisterFeature(FeatureModule{
		Order:   30,
		Name:    "sys",
		Summary: "/server/sys/{cpu,mem,disk,host}, /client/<host>/sys/{cpu,mem,host} [Read] リアルタイム負荷・OS・S.M.A.R.T.情報",
		ServerNodes: func(cfg *Config) []NodeDefinition {
			return []NodeDefinition{
				{
					Path: "server/sys/cpu",
					ReadFn: func() ([]byte, error) {
						return []byte(fmt.Sprintf("CPU: %.1f%% (%d Logical Cores)\n", getServerCPU(), runtime.NumCPU())), nil
					},
				},
				{
					Path: "server/sys/mem",
					ReadFn: func() ([]byte, error) {
						v, err := mem.VirtualMemory()
						if err != nil {
							return nil, err
						}
						uMB := v.Used / 1024 / 1024
						tMB := v.Total / 1024 / 1024
						return []byte(fmt.Sprintf("Mem: %.1f%% (%d MB / %d MB)\n", v.UsedPercent, uMB, tMB)), nil
					},
				},
				{
					Path: "server/sys/disk",
					ReadFn: func() ([]byte, error) {
						c := getLiveServerConfig()
						d := c.Server.Disk
						s := d.Smart
						return []byte(fmt.Sprintf(
							"Model:        %s\nSerial:       %s\nFirmware:     %s\nInterface:    %s (%s)\nCapacity:     %s (%s Total / %s Free)\nHealth:       %s (%d%%)\nTemperature:  %d C\nPowerOnHours: %d hours (%d cycles)\n",
							s.DeviceModel, s.SerialNumber, s.FirmwareVersion, s.Interface, s.TransferMode,
							d.CapacityDisplay, FormatBytesHuman(d.TotalBytes), FormatBytesHuman(d.FreeBytes),
							s.HealthStatus, s.HealthPercent, s.TemperatureC, s.PowerOnHours, s.PowerOnCount,
						)), nil
					},
				},
				{
					Path: "server/sys/host",
					ReadFn: func() ([]byte, error) {
						c := getLiveServerConfig()
						return []byte(fmt.Sprintf("Hostname:   %s\nOS:         %s (%s/%s)\nCores:      %d\nServerTime: %s\nRole:       9P Cluster Resource Server\n",
							c.Server.System.Hostname, c.Server.System.OSInfo, runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), time.Now().Format("2006-01-02 15:04:05"))), nil
					},
				},
			}
		},
		ClientNodes: func(ci *ClientInfo, tb *TreeBuilder) {
			hostname := ci.Hostname
			base := fmt.Sprintf("client/%s", hostname)

			_ = tb.RegisterRead(base+"/sys/cpu", func() ([]byte, error) {
				clientMu.RLock()
				c, ok := activeClients[hostname]
				clientMu.RUnlock()
				if !ok || time.Since(c.LastSeen) > 30*time.Second {
					return []byte(fmt.Sprintf("CPU: %.1f%% (offline / last: %s)\n", ci.CPUPercent, ci.LastSeen.Format("15:04:05"))), nil
				}
				return []byte(fmt.Sprintf("CPU: %.1f%%\n", c.CPUPercent)), nil
			})

			_ = tb.RegisterRead(base+"/sys/mem", func() ([]byte, error) {
				clientMu.RLock()
				c, ok := activeClients[hostname]
				clientMu.RUnlock()
				if !ok {
					return []byte("Mem: offline\n"), nil
				}
				pct := 0.0
				if c.MemTotalMB > 0 {
					pct = float64(c.MemUsedMB) * 100.0 / float64(c.MemTotalMB)
				}
				return []byte(fmt.Sprintf("Mem: %.1f%% (%d MB / %d MB)\n", pct, c.MemUsedMB, c.MemTotalMB)), nil
			})

			_ = tb.RegisterRead(base+"/sys/host", func() ([]byte, error) {
				clientMu.RLock()
				c, ok := activeClients[hostname]
				clientMu.RUnlock()
				if !ok {
					return []byte("Host: offline\n"), nil
				}
				status := "Online"
				if time.Since(c.LastSeen) > 30*time.Second {
					status = "Offline (heartbeat timeout)"
				}
				return []byte(fmt.Sprintf("Hostname:   %s\nOS:         %s\nStatus:     %s\nLastSeen:   %s\n",
					c.Hostname, c.OS, status, c.LastSeen.Format("2006-01-02 15:04:05"))), nil
			})
		},
	})
}

func getServerCPU() float64 {
	serverCPUMu.RLock()
	defer serverCPUMu.RUnlock()
	return serverCPUPercent
}
