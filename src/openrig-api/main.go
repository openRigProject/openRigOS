// openRig Management API + Web UI
//
// Runs permanently on port 7373 after provisioning.
// REST API at /api/* and management web UI at /.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	configPath = "/etc/openrig.json"
	listenAddr = ":7373"
	devMode    bool
)

// ── Config types ─────────────────────────────────────────────────────────

type Talkgroup struct {
	TG   int    `json:"tg"`
	Slot int    `json:"slot"`
	Name string `json:"name"`
}

type DMRConfig struct {
	Enabled    bool        `json:"enabled"`
	DMRID      int         `json:"dmr_id"`
	ColorCode  int         `json:"colorcode"`
	Network    string      `json:"network"`  // "brandmeister" | "dmrplus" | "freedmr" | "tgif" | "systemx" | "xlx" | "custom"
	Server     string      `json:"server"`
	Password   string      `json:"password"`
	Talkgroups []Talkgroup `json:"talkgroups"`
}

var validDMRNetworks = map[string]bool{
	"brandmeister": true, "dmrplus": true, "freedmr": true,
	"tgif": true, "systemx": true, "xlx": true, "custom": true,
}

var defaultDMRServers = map[string]string{
	"brandmeister": "us-west.brandmeister.network",
	"dmrplus":      "dmrplus.network",
	"freedmr":      "freedmr.net",
	"tgif":         "tgif.network",
	"systemx":      "xlx307.opendigital.radio",
	"xlx":          "",
	"custom":       "",
}

type YSFConfig struct {
	Enabled     bool   `json:"enabled"`
	Network     string `json:"network"`   // "ysf" | "fcs" | "custom"
	Reflector   string `json:"reflector"` // e.g. "AMERICA" for YSF, "FCS001" for FCS
	Module      string `json:"module"`    // FCS module letter: "A"-"Z" (FCS only)
	Suffix      string `json:"suffix"`
	Description string `json:"description"`
}

var validYSFNetworks = map[string]bool{
	"ysf": true, "fcs": true, "custom": true,
}

type CrossMode struct {
	YSF2DMREnabled   bool   `json:"ysf2dmr_enabled"`
	YSF2DMRTalkgroup int    `json:"ysf2dmr_talkgroup"`
	DMR2YSFEnabled   bool   `json:"dmr2ysf_enabled"`
	DMR2YSFRoom      string `json:"dmr2ysf_room"`
}

type ModemConfig struct {
	Type string `json:"type"` // e.g. "mmdvm_hs_hat", "zumspot", "dvmega"
	Port string `json:"port"` // serial port, e.g. /dev/ttyAMA0
}

type HotspotConfig struct {
	DMR         DMRConfig  `json:"dmr"`
	YSF         YSFConfig  `json:"ysf"`
	CrossMode   CrossMode  `json:"cross_mode"`
	Modem       ModemConfig `json:"modem"`
	RFFrequency float64    `json:"rf_frequency"` // MHz, e.g. 438.8000
	TXFrequency float64    `json:"tx_frequency"` // MHz, if split from RX
}

type WifiNetwork struct {
	SSID     string `json:"ssid"`
	Security string `json:"security"`
	Priority int    `json:"priority"`
	Password string `json:"password,omitempty"`
}

type WifiConfig struct {
	Networks []WifiNetwork `json:"networks"`
}

type DeviceConfig struct {
	Callsign   string `json:"callsign"`
	Hostname   string `json:"hostname"`
	Timezone   string `json:"timezone"`
	Name       string `json:"name"`
	GridSquare string `json:"grid_square"`
}

type StatusResponse struct {
	Provisioned bool    `json:"provisioned"`
	DeviceType  string  `json:"type"`
	Callsign    string  `json:"callsign"`
	Version     string  `json:"version"`
	Hostname    string  `json:"hostname"`
	Uptime      int     `json:"uptime"`
	CPUPercent  float64 `json:"cpu_percent"`
	MemTotalMB  int     `json:"mem_total_mb"`
	MemUsedMB   int     `json:"mem_used_mb"`
	DiskTotalGB float64 `json:"disk_total_gb"`
	DiskUsedGB  float64 `json:"disk_used_gb"`
}

type Client struct {
	Callsign  string `json:"callsign"`
	Mode      string `json:"mode"`
	LastHeard string `json:"last_heard"`
	Duration  string `json:"duration"`
}

type RigConfig struct {
	Enabled      bool   `json:"enabled"`
	HamlibModelID int   `json:"hamlib_model_id"`
	Port         string `json:"port"`
	Baud         int    `json:"baud"`
	DataBits     int    `json:"data_bits"`
	StopBits     int    `json:"stop_bits"`
	Parity       string `json:"parity"`
	Handshake    string `json:"handshake"`
}

type RigListResponse struct {
	Rigs []RigConfig `json:"rigs"`
}

var validBaudRates = map[int]bool{
	1200: true, 2400: true, 4800: true, 9600: true,
	19200: true, 38400: true, 57600: true, 115200: true,
}

var validParities = map[string]bool{
	"none": true, "even": true, "odd": true,
}

var validHandshakes = map[string]bool{
	"none": true, "hardware": true, "software": true,
}

// ── Config file access (thread-safe) ─────────────────────────────────────

var configMu sync.Mutex

func readConfig() (map[string]any, error) {
	configMu.Lock()
	defer configMu.Unlock()
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var cfg map[string]any
	return cfg, json.Unmarshal(data, &cfg)
}

func writeConfig(cfg map[string]any) error {
	configMu.Lock()
	defer configMu.Unlock()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0644)
}

// nested sets a value at a dot-separated path in a nested map.
func nested(m map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	for _, part := range parts[:len(parts)-1] {
		next, ok := m[part].(map[string]any)
		if !ok {
			next = make(map[string]any)
			m[part] = next
		}
		m = next
	}
	m[parts[len(parts)-1]] = value
}

// nestedGet retrieves a value at a dot-separated path. Returns nil if not found.
func nestedGet(m map[string]any, path string) any {
	parts := strings.Split(path, ".")
	for _, part := range parts[:len(parts)-1] {
		next, ok := m[part].(map[string]any)
		if !ok {
			return nil
		}
		m = next
	}
	return m[parts[len(parts)-1]]
}

func getString(m map[string]any, path, fallback string) string {
	v, ok := nestedGet(m, path).(string)
	if !ok || v == "" {
		return fallback
	}
	return v
}

func getBool(m map[string]any, path string) bool {
	v, _ := nestedGet(m, path).(bool)
	return v
}

func getFloat(m map[string]any, path string) float64 {
	v, _ := nestedGet(m, path).(float64)
	return v
}

// ── System helpers ───────────────────────────────────────────────────────

type systemMetrics struct {
	Uptime      int
	CPUPercent  float64
	MemTotalMB  int
	MemUsedMB   int
	DiskTotalGB float64
	DiskUsedGB  float64
}

func getSystemMetrics() systemMetrics {
	if devMode {
		return systemMetrics{
			Uptime:      3661,
			CPUPercent:  4.2,
			MemTotalMB:  512,
			MemUsedMB:   128,
			DiskTotalGB: 7.5,
			DiskUsedGB:  1.2,
		}
	}

	var m systemMetrics

	// Uptime from /proc/uptime (first float = seconds since boot)
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		var secs float64
		fmt.Sscanf(string(data), "%f", &secs)
		m.Uptime = int(secs)
	}

	// CPU usage: two samples of /proc/stat 200ms apart
	m.CPUPercent = readCPUPercent()

	// Memory from /proc/meminfo
	if f, err := os.Open("/proc/meminfo"); err == nil {
		defer f.Close()
		var memTotal, memAvailable int64
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "MemTotal:") {
				fmt.Sscanf(line, "MemTotal: %d kB", &memTotal)
			} else if strings.HasPrefix(line, "MemAvailable:") {
				fmt.Sscanf(line, "MemAvailable: %d kB", &memAvailable)
			}
		}
		m.MemTotalMB = int(memTotal / 1024)
		m.MemUsedMB = int((memTotal - memAvailable) / 1024)
	}

	// Disk usage via statfs
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err == nil {
		bsize := uint64(stat.Bsize)
		totalGB := float64(stat.Blocks*bsize) / (1024 * 1024 * 1024)
		freeGB := float64(stat.Bfree*bsize) / (1024 * 1024 * 1024)
		m.DiskTotalGB = math.Round(totalGB*10) / 10
		m.DiskUsedGB = math.Round((totalGB-freeGB)*10) / 10
	}

	return m
}

// readCPUPercent samples /proc/stat twice with a 200ms gap.
func readCPUPercent() float64 {
	parse := func() (idle, total uint64, ok bool) {
		data, err := os.ReadFile("/proc/stat")
		if err != nil {
			return 0, 0, false
		}
		// First line: cpu  user nice system idle iowait irq softirq steal
		line := strings.SplitN(string(data), "\n", 2)[0]
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "cpu" {
			return 0, 0, false
		}
		var sum uint64
		for i, f := range fields[1:] {
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				continue
			}
			sum += v
			if i == 3 { // idle is the 4th numeric field (index 3)
				idle = v
			}
		}
		return idle, sum, true
	}

	idle1, total1, ok1 := parse()
	if !ok1 {
		return 0
	}
	time.Sleep(200 * time.Millisecond)
	idle2, total2, ok2 := parse()
	if !ok2 {
		return 0
	}

	totalDelta := float64(total2 - total1)
	if totalDelta == 0 {
		return 0
	}
	idleDelta := float64(idle2 - idle1)
	pct := (1.0 - idleDelta/totalDelta) * 100.0
	return math.Round(pct*10) / 10
}

var validServices = map[string]string{
	"dmr":        "openrig-dmr.service",
	"ysf":        "openrig-ysf.service",
	"ysf2dmr":    "openrig-ysf2dmr.service",
	"dmr2ysf":    "openrig-dmr2ysf.service",
	"wifi":       "openrig-wifi.service",
	"mmdvmhost":  "openrig-mmdvmhost.service",
	"rigctld":    "openrig-rigctld.service",
	"dmrgateway": "openrig-dmrgateway.service",
	"ysfgateway": "openrig-ysfgateway.service",
}

func restartService(name string) error {
	if devMode {
		log.Printf("[dev] restartService(%q) skipped", name)
		return nil
	}
	unit, ok := validServices[name]
	if !ok {
		return fmt.Errorf("unknown service: %s", name)
	}
	return exec.Command("systemctl", "restart", unit).Run()
}

// ── WiFi helpers ─────────────────────────────────────────────────────────

var wpaConfPath = "/etc/wpa_supplicant/wpa_supplicant.conf"

func writeWPAConf(networks []WifiNetwork, country string) error {
	if devMode {
		log.Printf("[dev] writeWPAConf(%d networks, country=%q) skipped", len(networks), country)
		return nil
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, "country=%s\nctrl_interface=DIR=/var/run/wpa_supplicant GROUP=netdev\nupdate_config=1\n\n", country)
	for _, n := range networks {
		if n.SSID == "" || n.Password == "" {
			continue
		}
		switch n.Security {
		case "wpa3":
			fmt.Fprintf(&buf,
				"network={\n    ssid=%q\n    key_mgmt=SAE\n    psk=%q\n    ieee80211w=2\n    priority=%d\n}\n\n",
				n.SSID, n.Password, n.Priority)
		case "wpa2":
			fmt.Fprintf(&buf,
				"network={\n    ssid=%q\n    key_mgmt=WPA-PSK\n    psk=%q\n    priority=%d\n}\n\n",
				n.SSID, n.Password, n.Priority)
		default:
			fmt.Fprintf(&buf,
				"network={\n    ssid=%q\n    key_mgmt=WPA-PSK SAE\n    psk=%q\n    ieee80211w=1\n    priority=%d\n}\n\n",
				n.SSID, n.Password, n.Priority)
		}
	}
	return os.WriteFile(wpaConfPath, []byte(buf.String()), 0600)
}

// ── JSON response helpers ────────────────────────────────────────────────

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// ── API handlers ─────────────────────────────────────────────────────────

func handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, 405, "method not allowed")
		return
	}
	cfg, err := readConfig()
	if err != nil {
		jsonError(w, 500, "cannot read config")
		return
	}
	m := getSystemMetrics()
	jsonOK(w, StatusResponse{
		Provisioned: getBool(cfg, "openrig.device.provisioned"),
		DeviceType:  getString(cfg, "openrig.device.type", "unconfigured"),
		Callsign:    getString(cfg, "openrig.operator.callsign", ""),
		Version:     getString(cfg, "openrig.version", "0.1.0"),
		Hostname:    getString(cfg, "openrig.device.hostname", "openrig-config"),
		Uptime:      m.Uptime,
		CPUPercent:  m.CPUPercent,
		MemTotalMB:  m.MemTotalMB,
		MemUsedMB:   m.MemUsedMB,
		DiskTotalGB: m.DiskTotalGB,
		DiskUsedGB:  m.DiskUsedGB,
	})
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := readConfig()
	if err != nil {
		jsonError(w, 500, "cannot read config")
		return
	}

	switch r.Method {
	case http.MethodGet:
		jsonOK(w, DeviceConfig{
			Callsign:   getString(cfg, "openrig.operator.callsign", ""),
			Hostname:   getString(cfg, "openrig.device.hostname", ""),
			Timezone:   getString(cfg, "openrig.device.timezone", "UTC"),
			Name:       getString(cfg, "openrig.operator.name", ""),
			GridSquare: getString(cfg, "openrig.operator.grid_square", ""),
		})

	case http.MethodPut:
		var req DeviceConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, 400, "invalid JSON")
			return
		}
		if req.Callsign != "" {
			nested(cfg, "openrig.operator.callsign", strings.ToUpper(req.Callsign))
		}
		if req.Hostname != "" {
			re := regexp.MustCompile(`[^a-z0-9-]`)
			hostname := re.ReplaceAllString(strings.ToLower(req.Hostname), "")
			nested(cfg, "openrig.device.hostname", hostname)
			if !devMode {
				exec.Command("hostnamectl", "set-hostname", hostname).Run()
			}
		}
		if req.Timezone != "" {
			nested(cfg, "openrig.device.timezone", req.Timezone)
			if !devMode {
				exec.Command("timedatectl", "set-timezone", req.Timezone).Run()
			}
		}
		if req.Name != "" {
			nested(cfg, "openrig.operator.name", req.Name)
		}
		if req.GridSquare != "" {
			nested(cfg, "openrig.operator.grid_square", strings.ToUpper(req.GridSquare))
		}
		if err := writeConfig(cfg); err != nil {
			jsonError(w, 500, "cannot write config")
			return
		}
		jsonOK(w, map[string]string{"status": "ok"})

	default:
		jsonError(w, 405, "method not allowed")
	}
}

func handleHotspot(w http.ResponseWriter, r *http.Request) {
	cfg, err := readConfig()
	if err != nil {
		jsonError(w, 500, "cannot read config")
		return
	}

	switch r.Method {
	case http.MethodGet:
		hs := buildHotspotConfig(cfg)
		jsonOK(w, hs)

	case http.MethodPut:
		var req HotspotConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, 400, "invalid JSON")
			return
		}

		// Validate colorcode range
		if req.DMR.ColorCode < 1 || req.DMR.ColorCode > 15 {
			jsonError(w, 400, "colorcode must be 1-15")
			return
		}

		// Validate DMR network
		if req.DMR.Network != "" && !validDMRNetworks[req.DMR.Network] {
			jsonError(w, 400, fmt.Sprintf("invalid dmr network %q (valid: brandmeister, dmrplus, freedmr, tgif, systemx, xlx, custom)", req.DMR.Network))
			return
		}

		nested(cfg, "openrig.hotspot.dmr.enabled", req.DMR.Enabled)
		nested(cfg, "openrig.hotspot.dmr.dmr_id", req.DMR.DMRID)
		nested(cfg, "openrig.hotspot.dmr.colorcode", req.DMR.ColorCode)
		nested(cfg, "openrig.hotspot.dmr.network", req.DMR.Network)
		nested(cfg, "openrig.hotspot.dmr.server", req.DMR.Server)
		nested(cfg, "openrig.hotspot.dmr.password", req.DMR.Password)

		// Store talkgroups as []any for JSON serialization
		tgs := make([]any, len(req.DMR.Talkgroups))
		for i, tg := range req.DMR.Talkgroups {
			tgs[i] = map[string]any{"tg": tg.TG, "slot": tg.Slot, "name": tg.Name}
		}
		nested(cfg, "openrig.hotspot.dmr.talkgroups", tgs)

		// Validate YSF network
		if req.YSF.Network != "" && !validYSFNetworks[req.YSF.Network] {
			jsonError(w, 400, fmt.Sprintf("invalid ysf network %q (valid: ysf, fcs, custom)", req.YSF.Network))
			return
		}

		nested(cfg, "openrig.hotspot.ysf.enabled", req.YSF.Enabled)
		nested(cfg, "openrig.hotspot.ysf.network", req.YSF.Network)
		nested(cfg, "openrig.hotspot.ysf.reflector", req.YSF.Reflector)
		nested(cfg, "openrig.hotspot.ysf.module", req.YSF.Module)
		nested(cfg, "openrig.hotspot.ysf.suffix", req.YSF.Suffix)
		nested(cfg, "openrig.hotspot.ysf.description", req.YSF.Description)

		nested(cfg, "openrig.hotspot.cross_mode.ysf2dmr_enabled", req.CrossMode.YSF2DMREnabled)
		nested(cfg, "openrig.hotspot.cross_mode.ysf2dmr_talkgroup", req.CrossMode.YSF2DMRTalkgroup)
		nested(cfg, "openrig.hotspot.cross_mode.dmr2ysf_enabled", req.CrossMode.DMR2YSFEnabled)
		nested(cfg, "openrig.hotspot.cross_mode.dmr2ysf_room", req.CrossMode.DMR2YSFRoom)
		nested(cfg, "openrig.hotspot.rf_frequency", req.RFFrequency)
		nested(cfg, "openrig.hotspot.tx_frequency", req.TXFrequency)
		nested(cfg, "openrig.hotspot.modem.type", req.Modem.Type)
		nested(cfg, "openrig.hotspot.modem.port", req.Modem.Port)

		if err := writeConfig(cfg); err != nil {
			jsonError(w, 500, "cannot write config")
			return
		}

		// Update MMDVM.ini, DMRGateway.ini, YSFGateway.ini and restart affected services
		go func() {
			if !devMode {
				exec.Command("/usr/local/lib/openrig/mmdvm-update.sh").Run()
			}

			if req.DMR.Enabled {
				restartService("dmr")
				restartService("dmrgateway")
			}
			if req.YSF.Enabled {
				restartService("ysf")
				restartService("ysfgateway")
			}
			if req.CrossMode.YSF2DMREnabled {
				restartService("ysf2dmr")
			}
			if req.CrossMode.DMR2YSFEnabled {
				restartService("dmr2ysf")
			}
		}()

		jsonOK(w, map[string]string{"status": "ok"})

	default:
		jsonError(w, 405, "method not allowed")
	}
}

func buildHotspotConfig(cfg map[string]any) HotspotConfig {
	// Parse talkgroups from config
	var tgs []Talkgroup
	if raw, ok := nestedGet(cfg, "openrig.hotspot.dmr.talkgroups").([]any); ok {
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				tg := Talkgroup{}
				if v, ok := m["tg"].(float64); ok {
					tg.TG = int(v)
				}
				if v, ok := m["slot"].(float64); ok {
					tg.Slot = int(v)
				}
				if v, ok := m["name"].(string); ok {
					tg.Name = v
				}
				tgs = append(tgs, tg)
			}
		}
	}

	network := getString(cfg, "openrig.hotspot.dmr.network", "brandmeister")
	server := getString(cfg, "openrig.hotspot.dmr.server", "")
	if server == "" {
		server = defaultDMRServers[network]
	}

	return HotspotConfig{
		DMR: DMRConfig{
			Enabled:    getBool(cfg, "openrig.hotspot.dmr.enabled"),
			DMRID:      int(getFloat(cfg, "openrig.hotspot.dmr.dmr_id")),
			ColorCode:  int(getFloat(cfg, "openrig.hotspot.dmr.colorcode")),
			Network:    network,
			Server:     server,
			Password:   getString(cfg, "openrig.hotspot.dmr.password", ""),
			Talkgroups: tgs,
		},
		YSF: YSFConfig{
			Enabled:     getBool(cfg, "openrig.hotspot.ysf.enabled"),
			Network:     getString(cfg, "openrig.hotspot.ysf.network", "ysf"),
			Reflector:   getString(cfg, "openrig.hotspot.ysf.reflector", "AMERICA"),
			Module:      getString(cfg, "openrig.hotspot.ysf.module", ""),
			Suffix:      getString(cfg, "openrig.hotspot.ysf.suffix", ""),
			Description: getString(cfg, "openrig.hotspot.ysf.description", ""),
		},
		CrossMode: CrossMode{
			YSF2DMREnabled:   getBool(cfg, "openrig.hotspot.cross_mode.ysf2dmr_enabled"),
			YSF2DMRTalkgroup: int(getFloat(cfg, "openrig.hotspot.cross_mode.ysf2dmr_talkgroup")),
			DMR2YSFEnabled:   getBool(cfg, "openrig.hotspot.cross_mode.dmr2ysf_enabled"),
			DMR2YSFRoom:      getString(cfg, "openrig.hotspot.cross_mode.dmr2ysf_room", ""),
		},
		Modem: ModemConfig{
			Type: getString(cfg, "openrig.hotspot.modem.type", "mmdvm_hs_hat"),
			Port: getString(cfg, "openrig.hotspot.modem.port", "/dev/ttyAMA0"),
		},
		RFFrequency: getFloat(cfg, "openrig.hotspot.rf_frequency"),
		TXFrequency: getFloat(cfg, "openrig.hotspot.tx_frequency"),
	}
}

func handleWifi(w http.ResponseWriter, r *http.Request) {
	cfg, err := readConfig()
	if err != nil {
		jsonError(w, 500, "cannot read config")
		return
	}

	switch r.Method {
	case http.MethodGet:
		networks := buildWifiNetworks(cfg)
		jsonOK(w, WifiConfig{Networks: networks})

	case http.MethodPut:
		var req WifiConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, 400, "invalid JSON")
			return
		}

		// Validate passwords
		for _, n := range req.Networks {
			if n.Password != "" && len(n.Password) < 8 {
				jsonError(w, 400, fmt.Sprintf("WiFi password for %q must be at least 8 characters", n.SSID))
				return
			}
		}

		// Store networks in config (without passwords for persistence)
		netList := make([]any, len(req.Networks))
		for i, n := range req.Networks {
			netList[i] = map[string]any{
				"ssid":     n.SSID,
				"security": n.Security,
				"priority": n.Priority,
			}
		}
		nested(cfg, "openrig.network.wifi.networks", netList)
		if err := writeConfig(cfg); err != nil {
			jsonError(w, 500, "cannot write config")
			return
		}

		// Write wpa_supplicant.conf and restart wifi
		country := getString(cfg, "openrig.network.wifi.country", "US")
		if err := writeWPAConf(req.Networks, country); err != nil {
			jsonError(w, 500, "cannot write WiFi config")
			return
		}

		go restartService("wifi")

		jsonOK(w, map[string]string{"status": "ok"})

	default:
		jsonError(w, 405, "method not allowed")
	}
}

func buildWifiNetworks(cfg map[string]any) []WifiNetwork {
	var networks []WifiNetwork
	raw, ok := nestedGet(cfg, "openrig.network.wifi.networks").([]any)
	if !ok {
		return networks
	}
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			n := WifiNetwork{}
			if v, ok := m["ssid"].(string); ok {
				n.SSID = v
			}
			if v, ok := m["security"].(string); ok {
				n.Security = v
			}
			if v, ok := m["priority"].(float64); ok {
				n.Priority = int(v)
			}
			networks = append(networks, n)
		}
	}
	return networks
}

type ScannedNetwork struct {
	SSID     string `json:"ssid"`
	Signal   int    `json:"signal"`
	Security string `json:"security"`
}

func handleWifiScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, 405, "method not allowed")
		return
	}

	out, err := exec.Command("nmcli", "-t", "-f", "SSID,SIGNAL,SECURITY", "dev", "wifi", "list").CombinedOutput()
	if err != nil {
		jsonOK(w, map[string][]ScannedNetwork{"networks": {}})
		return
	}

	// Parse colon-delimited output, dedup by SSID keeping strongest signal
	best := make(map[string]ScannedNetwork)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 || parts[0] == "" {
			continue
		}
		ssid := parts[0]
		signal, _ := strconv.Atoi(parts[1])
		security := parts[2]
		if security == "" {
			security = "Open"
		}

		if existing, ok := best[ssid]; !ok || signal > existing.Signal {
			best[ssid] = ScannedNetwork{SSID: ssid, Signal: signal, Security: security}
		}
	}

	networks := make([]ScannedNetwork, 0, len(best))
	for _, n := range best {
		networks = append(networks, n)
	}
	// Sort by signal strength descending (nmcli returns 0-100 percentage)
	sort.Slice(networks, func(i, j int) bool {
		return networks[i].Signal > networks[j].Signal
	})

	jsonOK(w, map[string][]ScannedNetwork{"networks": networks})
}

type NetworkStatus struct {
	Mode      string `json:"mode"`
	SSID      string `json:"ssid"`
	IP        string `json:"ip"`
	SignalDBM int    `json:"signal_dbm"`
	Connected bool   `json:"connected"`
	Interface string `json:"interface"`
}

func handleNetwork(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, 405, "method not allowed")
		return
	}
	jsonOK(w, getNetworkStatus())
}

func getNetworkStatus() NetworkStatus {
	var ns NetworkStatus

	// Find default route interface from /proc/net/route
	// Format: Iface Destination Gateway Flags ... (tab-separated)
	// Flag 0x0003 = RTF_UP | RTF_GATEWAY
	iface := findDefaultRouteIface()

	if iface == "" {
		// No default route — check for AP mode
		if isHostapdRunning() {
			ns.Mode = "ap"
			ns.Interface = "wlan0"
			ns.IP = getIfaceIP("wlan0")
			ns.Connected = ns.IP != ""
		} else {
			ns.Mode = "none"
		}
		return ns
	}

	ns.Interface = iface
	ns.IP = getIfaceIP(iface)
	ns.Connected = ns.IP != ""

	if strings.HasPrefix(iface, "wlan") || strings.HasPrefix(iface, "wifi") {
		ns.Mode = "wifi"
		ns.SSID = getWifiSSID(iface)
		ns.SignalDBM = getWifiSignalDBM(iface)
	} else if strings.HasPrefix(iface, "eth") {
		ns.Mode = "ethernet"
	} else {
		ns.Mode = "wifi" // default for unknown wireless interfaces
	}

	return ns
}

func findDefaultRouteIface() string {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Scan() // skip header
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		// Destination == 00000000 means default route
		if fields[1] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil {
			continue
		}
		// RTF_UP (0x0001) | RTF_GATEWAY (0x0002) = 0x0003
		if flags&0x0003 == 0x0003 {
			return fields[0]
		}
	}
	return ""
}

func getIfaceIP(iface string) string {
	out, err := exec.Command("ip", "-4", "-o", "addr", "show", iface).CombinedOutput()
	if err != nil {
		return ""
	}
	// Output: "2: wlan0    inet 192.168.1.42/24 ..."
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "inet" && i+1 < len(fields) {
				addr := fields[i+1]
				// Strip CIDR prefix
				if idx := strings.Index(addr, "/"); idx >= 0 {
					addr = addr[:idx]
				}
				return addr
			}
		}
	}
	return ""
}

func getWifiSSID(iface string) string {
	out, err := exec.Command("iwgetid", "-r", iface).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func getWifiSignalDBM(iface string) int {
	out, err := exec.Command("iwconfig", iface).CombinedOutput()
	if err != nil {
		return 0
	}
	// Look for "Signal level=-65 dBm" or "Signal level:-65 dBm"
	re := regexp.MustCompile(`Signal level[=:](-?\d+)`)
	m := re.FindSubmatch(out)
	if m == nil {
		return 0
	}
	v, err := strconv.Atoi(string(m[1]))
	if err != nil {
		return 0
	}
	return v
}

func isHostapdRunning() bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Only check numeric (PID) directories
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil {
			continue
		}
		// cmdline is null-separated; check if first arg contains "hostapd"
		if strings.Contains(string(data), "hostapd") {
			return true
		}
	}
	return false
}

func handleClients(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, 405, "method not allowed")
		return
	}

	// In a real deployment, this would query MMDVM or YSF gateway logs.
	// For now, return an empty list. The structure is ready for integration.
	clients := []Client{}
	jsonOK(w, map[string]any{"clients": clients})
}

func handleServiceRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, 405, "method not allowed")
		return
	}

	// Extract service name from path: /api/services/{name}/restart
	path := strings.TrimPrefix(r.URL.Path, "/api/services/")
	name := strings.TrimSuffix(path, "/restart")

	if _, ok := validServices[name]; !ok {
		jsonError(w, 400, fmt.Sprintf("unknown service: %s (valid: dmr, ysf, ysf2dmr, dmr2ysf, wifi, mmdvmhost, rigctld, dmrgateway, ysfgateway)", name))
		return
	}

	if err := restartService(name); err != nil {
		jsonError(w, 500, fmt.Sprintf("restart failed: %v", err))
		return
	}

	jsonOK(w, map[string]string{"status": "ok", "service": name})
}

func handleReboot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, 405, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"ok":true}`))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if !devMode {
		go func() {
			time.Sleep(500 * time.Millisecond)
			exec.Command("systemctl", "reboot").Run()
		}()
	} else {
		log.Println("[dev] reboot skipped")
	}
}

func handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, 405, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"ok":true}`))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if !devMode {
		go func() {
			time.Sleep(500 * time.Millisecond)
			exec.Command("systemctl", "poweroff").Run()
		}()
	} else {
		log.Println("[dev] poweroff skipped")
	}
}

func handleRig(w http.ResponseWriter, r *http.Request) {
	cfg, err := readConfig()
	if err != nil {
		jsonError(w, 500, "cannot read config")
		return
	}

	switch r.Method {
	case http.MethodGet:
		rigs := buildRigList(cfg)
		jsonOK(w, RigListResponse{Rigs: rigs})

	case http.MethodPut:
		var req RigListResponse
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, 400, "invalid JSON")
			return
		}

		// Validate each rig
		for i, rig := range req.Rigs {
			if rig.HamlibModelID < 1 {
				jsonError(w, 400, fmt.Sprintf("rig[%d]: hamlib_model_id must be a positive integer", i))
				return
			}
			if !validBaudRates[rig.Baud] {
				jsonError(w, 400, fmt.Sprintf("rig[%d]: baud must be one of 1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200", i))
				return
			}
			if !validParities[rig.Parity] {
				jsonError(w, 400, fmt.Sprintf("rig[%d]: parity must be none, even, or odd", i))
				return
			}
			if !validHandshakes[rig.Handshake] {
				jsonError(w, 400, fmt.Sprintf("rig[%d]: handshake must be none, hardware, or software", i))
				return
			}
		}

		// Convert to []any for JSON storage
		rigList := make([]any, len(req.Rigs))
		for i, rig := range req.Rigs {
			rigList[i] = map[string]any{
				"id":              fmt.Sprintf("rig%d", i+1),
				"enabled":         rig.Enabled,
				"hamlib_model_id": rig.HamlibModelID,
				"port":            rig.Port,
				"baud":            rig.Baud,
				"data_bits":       rig.DataBits,
				"stop_bits":       rig.StopBits,
				"parity":          rig.Parity,
				"handshake":       rig.Handshake,
			}
		}
		nested(cfg, "openrig.radio.rigs", rigList)

		if err := writeConfig(cfg); err != nil {
			jsonError(w, 500, "cannot write config")
			return
		}

		go restartService("rigctld")

		jsonOK(w, map[string]string{"status": "ok"})

	default:
		jsonError(w, 405, "method not allowed")
	}
}

func buildRigList(cfg map[string]any) []RigConfig {
	var rigs []RigConfig
	raw, ok := nestedGet(cfg, "openrig.radio.rigs").([]any)
	if !ok {
		return rigs
	}
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		rig := RigConfig{
			Port:      "/dev/ttyUSB0",
			Baud:      9600,
			DataBits:  8,
			StopBits:  1,
			Parity:    "none",
			Handshake: "none",
		}
		if v, ok := m["enabled"].(bool); ok {
			rig.Enabled = v
		}
		if v, ok := m["hamlib_model_id"].(float64); ok {
			rig.HamlibModelID = int(v)
		}
		if v, ok := m["port"].(string); ok && v != "" {
			rig.Port = v
		}
		if v, ok := m["baud"].(float64); ok && v > 0 {
			rig.Baud = int(v)
		}
		if v, ok := m["data_bits"].(float64); ok && v > 0 {
			rig.DataBits = int(v)
		}
		if v, ok := m["stop_bits"].(float64); ok && v > 0 {
			rig.StopBits = int(v)
		}
		if v, ok := m["parity"].(string); ok && v != "" {
			rig.Parity = v
		}
		if v, ok := m["handshake"].(string); ok && v != "" {
			rig.Handshake = v
		}
		rigs = append(rigs, rig)
	}
	return rigs
}

// ── DMR ID endpoint ──────────────────────────────────────────────────────

func handleDmrId(w http.ResponseWriter, r *http.Request) {
	cfg, err := readConfig()
	if err != nil {
		jsonError(w, 500, "cannot read config")
		return
	}

	switch r.Method {
	case http.MethodGet:
		dmrID := getFloat(cfg, "openrig.operator.dmr_id")
		jsonOK(w, map[string]int{"dmr_id": int(dmrID)})

	case http.MethodPut:
		var req struct {
			DMRID int `json:"dmr_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, 400, "invalid JSON")
			return
		}

		if req.DMRID < 1000000 || req.DMRID > 9999999 {
			jsonError(w, 400, "dmr_id must be a 7-digit number (1000000-9999999)")
			return
		}

		nested(cfg, "openrig.operator.dmr_id", req.DMRID)

		if err := writeConfig(cfg); err != nil {
			jsonError(w, 500, "cannot write config")
			return
		}

		// Update gateway configs with new DMR ID
		if !devMode {
			go exec.Command("/usr/local/lib/openrig/mmdvm-update.sh").Run()
		}

		jsonOK(w, map[string]string{"status": "ok"})

	default:
		jsonError(w, 405, "method not allowed")
	}
}


// ── Last-heard endpoint ──────────────────────────────────────────────────

type LastHeardEntry struct {
	Callsign  string `json:"callsign"`
	Mode      string `json:"mode"`      // "DMR" or "YSF"
	Info      string `json:"info"`      // TG number for DMR, room name for YSF
	Duration  string `json:"duration"`  // e.g. "12s"
	Timestamp string `json:"timestamp"` // RFC3339
}

func handleLastHeard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, 405, "method not allowed")
		return
	}

	if devMode {
		now := time.Now()
		entries := []LastHeardEntry{
			{Callsign: "W1ABC", Mode: "DMR", Info: "3100", Duration: "13s", Timestamp: now.Add(-2 * time.Minute).Format(time.RFC3339)},
			{Callsign: "K5XYZ", Mode: "YSF", Info: "AMERICA", Duration: "8s", Timestamp: now.Add(-5 * time.Minute).Format(time.RFC3339)},
			{Callsign: "N3DEF", Mode: "DMR", Info: "91", Duration: "22s", Timestamp: now.Add(-11 * time.Minute).Format(time.RFC3339)},
			{Callsign: "VE3RST", Mode: "DMR", Info: "302", Duration: "5s", Timestamp: now.Add(-18 * time.Minute).Format(time.RFC3339)},
			{Callsign: "JA1QRS", Mode: "YSF", Info: "FCS001-A", Duration: "15s", Timestamp: now.Add(-30 * time.Minute).Format(time.RFC3339)},
		}
		jsonOK(w, map[string]any{"entries": entries})
		return
	}

	entries := parseMMDVMLastHeard()
	jsonOK(w, map[string]any{"entries": entries})
}

var (
	reMMDVMEnd = regexp.MustCompile(
		`^M: (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\.\d+ ` +
			`(DMR|YSF).*end of.*transmission from ([A-Z0-9/]+) to (?:TG )?(\S+).*?(\d+(?:\.\d+)?) seconds`)
)

func parseMMDVMLastHeard() []LastHeardEntry {
	// Find the most recent MMDVM log file
	matches, err := filepath.Glob("/var/log/mmdvmhost/MMDVM-*.log")
	if err != nil || len(matches) == 0 {
		return []LastHeardEntry{}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	logFile := matches[0]

	data, err := os.ReadFile(logFile)
	if err != nil {
		return []LastHeardEntry{}
	}

	lines := strings.Split(string(data), "\n")
	// Take last 500 lines
	start := len(lines) - 500
	if start < 0 {
		start = 0
	}
	lines = lines[start:]

	var entries []LastHeardEntry
	for i := len(lines) - 1; i >= 0 && len(entries) < 10; i-- {
		m := reMMDVMEnd.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		ts, err := time.Parse("2006-01-02 15:04:05", m[1])
		if err != nil {
			continue
		}
		secs, _ := strconv.ParseFloat(m[5], 64)
		entries = append(entries, LastHeardEntry{
			Callsign:  m[3],
			Mode:      m[2],
			Info:      m[4],
			Duration:  fmt.Sprintf("%.0fs", secs),
			Timestamp: ts.Format(time.RFC3339),
		})
	}
	return entries
}

// ── DMR server list ──────────────────────────────────────────────────────

var hardcodedServers = map[string][]string{
	"ysf": {
		"AMERICA", "US-WEST", "US-EAST", "US-SOUTH", "US-MIDWEST",
		"YSF-FUSION", "PARROT",
	},
	"fcs": {
		"FCS001", "FCS002", "FCS003", "FCS100", "FCS222", "FCS232", "FCS300",
	},
	"brandmeister": {
		"us-west.brandmeister.network",
		"us-east.brandmeister.network",
		"us-central.brandmeister.network",
		"ca.brandmeister.network",
		"uk.brandmeister.network",
		"eu-west.brandmeister.network",
		"eu-east.brandmeister.network",
		"eu-central.brandmeister.network",
		"eu-north.brandmeister.network",
		"au.brandmeister.network",
		"as.brandmeister.network",
		"af.brandmeister.network",
		"sa.brandmeister.network",
		"mena.brandmeister.network",
		"russia.brandmeister.network",
	},
	"dmrplus": {
		"master-eu.xreflector.net",
		"master-eu2.xreflector.net",
		"master-us.xreflector.net",
		"master-ap.xreflector.net",
	},
	"freedmr": {
		"freedmr.net",
		"uk.freedmr.net",
		"aus.freedmr.net",
	},
	"tgif": {"tgif.network"},
	"systemx": {"xlx307.opendigital.radio"},
}

var (
	srvCacheMu  sync.Mutex
	srvCacheVal = make(map[string][]string)
	srvCacheExp = make(map[string]time.Time)
)

func getCachedServers(network string) ([]string, bool) {
	srvCacheMu.Lock()
	defer srvCacheMu.Unlock()
	exp, ok := srvCacheExp[network]
	if !ok || time.Now().After(exp) {
		return nil, false
	}
	return srvCacheVal[network], true
}

func setCachedServers(network string, servers []string) {
	srvCacheMu.Lock()
	defer srvCacheMu.Unlock()
	srvCacheVal[network] = servers
	srvCacheExp[network] = time.Now().Add(time.Hour)
}

// fetchHostsFile fetches a semicolon-delimited hosts file (YSFHosts.txt / FCSHosts.txt)
// and returns the first field of each non-comment line — the reflector/room name.
func fetchHostsFile(url string, fallback []string) []string {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil || resp.StatusCode != 200 {
		return fallback
	}
	defer resp.Body.Close()
	seen := make(map[string]bool)
	var names []string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ";", 2)
		name := strings.TrimSpace(parts[0])
		if name != "" && !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	if len(names) == 0 {
		return fallback
	}
	sort.Strings(names)
	return names
}

func fetchBrandmeisterServers() []string {
	fallback := hardcodedServers["brandmeister"]
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("https://api.brandmeister.network/v2/server")
	if err != nil || resp.StatusCode != 200 {
		return fallback
	}
	defer resp.Body.Close()
	var data []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return fallback
	}
	seen := make(map[string]bool)
	var servers []string
	for _, item := range data {
		var host string
		for _, key := range []string{"server", "host", "hostname", "address"} {
			if v, ok := item[key].(string); ok && v != "" {
				host = v
				break
			}
		}
		if host != "" && !seen[host] {
			servers = append(servers, host)
			seen[host] = true
		}
	}
	if len(servers) == 0 {
		return fallback
	}
	sort.Strings(servers)
	return servers
}

func handleHotspotServers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, 405, "method not allowed")
		return
	}
	network := r.URL.Query().Get("network")
	if servers, ok := getCachedServers(network); ok {
		jsonOK(w, map[string][]string{"servers": servers})
		return
	}
	var servers []string
	switch {
	case network == "brandmeister" && !devMode:
		servers = fetchBrandmeisterServers()
	case network == "ysf" && !devMode:
		servers = fetchHostsFile("https://www.pistar.uk/downloads/YSFHosts.txt", hardcodedServers["ysf"])
	case network == "fcs" && !devMode:
		servers = fetchHostsFile("https://www.pistar.uk/downloads/FCSHosts.txt", hardcodedServers["fcs"])
	default:
		if s, ok := hardcodedServers[network]; ok {
			servers = s
		} else {
			servers = []string{}
		}
	}
	setCachedServers(network, servers)
	jsonOK(w, map[string][]string{"servers": servers})
}

// ── Main ─────────────────────────────────────────────────────────────────

func main() {
	flag.BoolVar(&devMode, "dev", false, "Run in local dev mode: use ./openrig.json, stub system calls")
	flag.Parse()

	if devMode {
		configPath = "./openrig.json"
		wpaConfPath = "./wpa_supplicant-dev.conf"
		// Seed a local config if none exists
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			seed := []byte(`{"openrig":{"device":{"provisioned":true,"type":"hotspot","hostname":"dev-hotspot","timezone":"UTC"},"operator":{"callsign":"N0CALL","name":"Dev User","grid_square":"FN31","country":"US"},"management":{"api_enabled":true,"mdns_enabled":true},"hotspot":{"dmr":{"enabled":true,"dmr_id":1234567,"colorcode":1,"network":"brandmeister","server":"us-west.brandmeister.network","password":"","talkgroups":[{"tg":91,"slot":1,"name":"Worldwide"},{"tg":3100,"slot":2,"name":"USA"}]},"ysf":{"enabled":false,"network":"ysf","reflector":"AMERICA","module":"","suffix":"-OR","description":"Dev hotspot"},"cross_mode":{"ysf2dmr_enabled":false,"ysf2dmr_talkgroup":91,"dmr2ysf_enabled":false,"dmr2ysf_room":""},"rf_frequency":438.8},"network":{"wifi":{"country":"US","networks":[{"ssid":"HomeNetwork","security":"auto","priority":10}]}}}}`)
			if err := os.WriteFile(configPath, seed, 0644); err != nil {
				log.Fatalf("Cannot create %s: %v", configPath, err)
			}
			log.Printf("[dev] Created seed config at %s", configPath)
		}
		log.Printf("[dev] Dev mode enabled — config: %s, addr: %s", configPath, listenAddr)
	}

	mux := http.NewServeMux()

	// REST API only — web UI is served by openrig-webprovision on port 80
	mux.HandleFunc("/api/status", handleStatus)
	mux.HandleFunc("/api/config", handleConfig)
	mux.HandleFunc("/api/hotspot", handleHotspot)
	mux.HandleFunc("/api/wifi", handleWifi)
	mux.HandleFunc("/api/wifi/scan", handleWifiScan)
	mux.HandleFunc("/api/network", handleNetwork)
	mux.HandleFunc("/api/rig", handleRig)
	mux.HandleFunc("/api/dmrid", handleDmrId)
	mux.HandleFunc("/api/clients", handleClients)
	mux.HandleFunc("/api/lastheard", handleLastHeard)
	mux.HandleFunc("/api/hotspot/servers", handleHotspotServers)
	// Service restart: matches /api/services/{name}/restart
	mux.HandleFunc("/api/services/", handleServiceRestart)
	mux.HandleFunc("/api/reboot", handleReboot)
	mux.HandleFunc("/api/shutdown", handleShutdown)

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	log.Printf("openRig API listening on %s", listenAddr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
