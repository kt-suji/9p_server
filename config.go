package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server ServerConfig `yaml:"server"`
}

type ServerConfig struct {
	ListenAddr string       `yaml:"listen_addr"`
	System     SystemConfig `yaml:"system"`
	Disk       DiskConfig   `yaml:"disk"`
	Memory     MemoryConfig `yaml:"memory"`
}

type SystemConfig struct {
	Hostname string `yaml:"hostname"`
	OSInfo   string `yaml:"os_info"`
}

type DiskConfig struct {
	CapacityDisplay string      `yaml:"capacity_display"`
	UsedDisplay     string      `yaml:"used_display"`
	TotalBytes      uint64      `yaml:"total_bytes"`
	FreeBytes       uint64      `yaml:"free_bytes"`
	Smart           SmartConfig `yaml:"smart"`
}

type MemoryConfig struct {
	TotalDisplay string  `yaml:"total_display"`
	UsedPercent  float64 `yaml:"used_percent"`
	VirtualMode  bool    `yaml:"virtual_mode"`
}

type SmartConfig struct {
	DeviceModel        string  `yaml:"device_model"`
	SerialNumber       string  `yaml:"serial_number"`
	FirmwareVersion    string  `yaml:"firmware_version"`
	Interface          string  `yaml:"interface"`
	TransferMode       string  `yaml:"transfer_mode"`
	HealthStatus       string  `yaml:"health_status"`
	HealthPercent      int     `yaml:"health_percent"`
	TemperatureC       int     `yaml:"temperature_c"`
	PowerOnHours       int     `yaml:"power_on_hours"`
	PowerOnCount       int     `yaml:"power_on_count"`
	UnsafeShutdowns    int     `yaml:"unsafe_shutdowns"`
	ReallocatedSectors int     `yaml:"reallocated_sectors"`
	AvailableSpare     int     `yaml:"available_spare"`
	TotalReadsTB       float64 `yaml:"total_reads_tb"`
	TotalWritesTB      float64 `yaml:"total_writes_tb"`
}

var (
	liveConfigMu  sync.RWMutex
	liveConfig    *Config
	liveConfigPath string
	liveModTime   time.Time
)

func ParseCapacityToBytes(str string) uint64 {
	str = strings.TrimSpace(str)
	if str == "" {
		return 1000 * 1024 * 1024 * 1024 // 1000GB (1TB) default
	}
	upper := strings.ToUpper(str)
	// デフォルト単位はGB (1000 = 1000GB = 1TB, 0.1 = 100MB, 2000000 = 2PB)
	var multiplier uint64 = 1024 * 1024 * 1024
	var numStr string

	if strings.HasSuffix(upper, "PB") || strings.HasSuffix(upper, "PIB") {
		multiplier = 1024 * 1024 * 1024 * 1024 * 1024
		numStr = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(upper, "PIB"), "PB"))
	} else if strings.HasSuffix(upper, "P") {
		multiplier = 1024 * 1024 * 1024 * 1024 * 1024
		numStr = strings.TrimSpace(strings.TrimSuffix(upper, "P"))
	} else if strings.HasSuffix(upper, "TB") || strings.HasSuffix(upper, "TIB") {
		multiplier = 1024 * 1024 * 1024 * 1024
		numStr = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(upper, "TIB"), "TB"))
	} else if strings.HasSuffix(upper, "T") {
		multiplier = 1024 * 1024 * 1024 * 1024
		numStr = strings.TrimSpace(strings.TrimSuffix(upper, "T"))
	} else if strings.HasSuffix(upper, "GB") || strings.HasSuffix(upper, "GIB") {
		multiplier = 1024 * 1024 * 1024
		numStr = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(upper, "GIB"), "GB"))
	} else if strings.HasSuffix(upper, "G") {
		multiplier = 1024 * 1024 * 1024
		numStr = strings.TrimSpace(strings.TrimSuffix(upper, "G"))
	} else if strings.HasSuffix(upper, "MB") || strings.HasSuffix(upper, "MIB") {
		multiplier = 1024 * 1024
		numStr = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(upper, "MIB"), "MB"))
	} else if strings.HasSuffix(upper, "M") {
		multiplier = 1024 * 1024
		numStr = strings.TrimSpace(strings.TrimSuffix(upper, "M"))
	} else if strings.HasSuffix(upper, "KB") || strings.HasSuffix(upper, "KIB") {
		multiplier = 1024
		numStr = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(upper, "KIB"), "KB"))
	} else if strings.HasSuffix(upper, "K") {
		multiplier = 1024
		numStr = strings.TrimSpace(strings.TrimSuffix(upper, "K"))
	} else {
		numStr = upper
	}

	val, err := strconv.ParseFloat(numStr, 64)
	if err != nil || val <= 0 {
		return 1000 * 1024 * 1024 * 1024 // 1000GB default
	}
	return uint64(val * float64(multiplier))
}

func FormatBytesHuman(b uint64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
		TB = 1024 * GB
		PB = 1024 * TB
	)
	switch {
	case b >= PB:
		return fmt.Sprintf("%.2f PB", float64(b)/float64(PB))
	case b >= TB:
		return fmt.Sprintf("%.2f TB", float64(b)/float64(TB))
	case b >= GB:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.2f MB", float64(b)/float64(MB))
	default:
		return fmt.Sprintf("%d bytes", b)
	}
}

func loadServerConfig(path string) *Config {
	cfg := &Config{
		Server: ServerConfig{
			ListenAddr: ":5640",
			System: SystemConfig{
				Hostname: "winserver",
				OSInfo:   "Windows",
			},
			Disk: DiskConfig{
				CapacityDisplay: "1PB",
				TotalBytes:      1125899906842624,
				FreeBytes:       1099511627776000,
				Smart: SmartConfig{
					DeviceModel:     "Generic 9P Volume",
					HealthStatus:    "GOOD",
					TemperatureC:    36,
					PowerOnHours:    1000,
					AvailableSpare:  100,
				},
			},
			Memory: MemoryConfig{
				TotalDisplay: "64GB",
				UsedPercent:  28.5,
				VirtualMode:  true,
			},
		},
	}

	targetPath := path
	data, err := os.ReadFile(targetPath)
	if err != nil {
		targetPath = "../" + path
		data, err = os.ReadFile(targetPath)
	}

	if err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			log.Printf("[WARN] config.yaml パース失敗: %v (デフォルト設定を使用します)", err)
		} else {
			log.Printf("[CONFIG] 設定ファイルを読み込みました: %s", targetPath)
			if fi, err := os.Stat(targetPath); err == nil {
				liveModTime = fi.ModTime()
			}
		}
	} else {
		log.Println("[INFO] config.yaml が見つからないためデフォルト設定で起動します")
	}

	// capacity_display から total_bytes を自動補完
	if cfg.Server.Disk.CapacityDisplay != "" && (cfg.Server.Disk.TotalBytes == 0 || cfg.Server.Disk.TotalBytes == 1125899906842624) {
		calcBytes := ParseCapacityToBytes(cfg.Server.Disk.CapacityDisplay)
		if calcBytes > 0 {
			cfg.Server.Disk.TotalBytes = calcBytes
			if cfg.Server.Disk.UsedDisplay == "" && (cfg.Server.Disk.FreeBytes == 0 || cfg.Server.Disk.FreeBytes == 1099511627776000) {
				cfg.Server.Disk.FreeBytes = calcBytes * 98 / 100
			}
		}
	}

	liveConfigMu.Lock()
	liveConfig = cfg
	liveConfigPath = targetPath
	liveConfigMu.Unlock()

	return cfg
}

func getLiveServerConfig() *Config {
	liveConfigMu.Lock()
	defer liveConfigMu.Unlock()

	if liveConfigPath != "" {
		if fi, err := os.Stat(liveConfigPath); err == nil {
			if fi.ModTime().After(liveModTime) {
				if d, err := os.ReadFile(liveConfigPath); err == nil {
					var newCfg Config
					if err := yaml.Unmarshal(d, &newCfg); err == nil {
						// 自動計算
						if newCfg.Server.Disk.CapacityDisplay != "" {
							calcBytes := ParseCapacityToBytes(newCfg.Server.Disk.CapacityDisplay)
							if calcBytes > 0 {
								newCfg.Server.Disk.TotalBytes = calcBytes
								if newCfg.Server.Disk.UsedDisplay == "" && newCfg.Server.Disk.FreeBytes == 0 {
									newCfg.Server.Disk.FreeBytes = calcBytes * 98 / 100
								}
							}
						}
						liveConfig = &newCfg
						liveModTime = fi.ModTime()
						log.Printf("[CONFIG] 設定ファイルを動的リロードしました: %s (容量: %s, メモリ: %s)",
							liveConfigPath, liveConfig.Server.Disk.CapacityDisplay, liveConfig.Server.Memory.TotalDisplay)
					}
				}
			}
		}
	}

	return liveConfig
}
