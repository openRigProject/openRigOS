// openRig Management API
//
// Runs permanently on port 7373 after provisioning.
// ConnectRPC services at /openrig.v1.*/.
package main

import (
	"bufio"
	"context"
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

	"connectrpc.com/connect"
	"connectrpc.com/grpcreflect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	openrigv1 "openrig/gen/openrig/v1"
	"openrig/gen/openrig/v1/openrigv1connect"
)

var (
	configPath = "/etc/openrig.json"
	listenAddr = ":7373"
	devMode    bool
)

// ── Config types (JSON file structs) ─────────────────────────────────────

type jsonTalkgroup struct {
	TG   int    `json:"tg"`
	Slot int    `json:"slot"`
	Name string `json:"name"`
}

type jsonDMRConfig struct {
	Enabled    bool            `json:"enabled"`
	DMRID      int             `json:"dmr_id"`
	ColorCode  int             `json:"colorcode"`
	Network    string          `json:"network"`
	Server     string          `json:"server"`
	Password   string          `json:"password"`
	Talkgroups []jsonTalkgroup `json:"talkgroups"`
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

type jsonYSFConfig struct {
	Enabled     bool   `json:"enabled"`
	Network     string `json:"network"`
	Reflector   string `json:"reflector"`
	Module      string `json:"module"`
	Suffix      string `json:"suffix"`
	Description string `json:"description"`
}

var validYSFNetworks = map[string]bool{
	"ysf": true, "fcs": true, "custom": true,
}

type jsonCrossMode struct {
	YSF2DMREnabled   bool   `json:"ysf2dmr_enabled"`
	YSF2DMRTalkgroup int    `json:"ysf2dmr_talkgroup"`
	DMR2YSFEnabled   bool   `json:"dmr2ysf_enabled"`
	DMR2YSFRoom      string `json:"dmr2ysf_room"`
}

type jsonModemConfig struct {
	Type string `json:"type"`
	Port string `json:"port"`
}

type jsonHotspotConfig struct {
	DMR         jsonDMRConfig `json:"dmr"`
	YSF         jsonYSFConfig `json:"ysf"`
	CrossMode   jsonCrossMode `json:"cross_mode"`
	Modem       jsonModemConfig `json:"modem"`
	RFFrequency float64       `json:"rf_frequency"`
	TXFrequency float64       `json:"tx_frequency"`
}

type jsonWifiNetwork struct {
	SSID     string `json:"ssid"`
	Security string `json:"security"`
	Priority int    `json:"priority"`
	Password string `json:"password,omitempty"`
}

type jsonRigConfig struct {
	Enabled       bool   `json:"enabled"`
	HamlibModelID int    `json:"hamlib_model_id"`
	Port          string `json:"port"`
	Baud          int    `json:"baud"`
	DataBits      int    `json:"data_bits"`
	StopBits      int    `json:"stop_bits"`
	Parity        string `json:"parity"`
	Handshake     string `json:"handshake"`
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

	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		var secs float64
		fmt.Sscanf(string(data), "%f", &secs)
		m.Uptime = int(secs)
	}

	m.CPUPercent = readCPUPercent()

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

func readCPUPercent() float64 {
	parse := func() (idle, total uint64, ok bool) {
		data, err := os.ReadFile("/proc/stat")
		if err != nil {
			return 0, 0, false
		}
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
			if i == 3 {
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

func doRestartService(name string) error {
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

func writeWPAConf(networks []*openrigv1.WifiNetwork, country string) error {
	if devMode {
		log.Printf("[dev] writeWPAConf(%d networks, country=%q) skipped", len(networks), country)
		return nil
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, "country=%s\nctrl_interface=DIR=/var/run/wpa_supplicant GROUP=netdev\nupdate_config=1\n\n", country)
	for _, n := range networks {
		if n.Ssid == "" || n.Password == "" {
			continue
		}
		switch n.Security {
		case "wpa3":
			fmt.Fprintf(&buf,
				"network={\n    ssid=%q\n    key_mgmt=SAE\n    psk=%q\n    ieee80211w=2\n    priority=%d\n}\n\n",
				n.Ssid, n.Password, n.Priority)
		case "wpa2":
			fmt.Fprintf(&buf,
				"network={\n    ssid=%q\n    key_mgmt=WPA-PSK\n    psk=%q\n    priority=%d\n}\n\n",
				n.Ssid, n.Password, n.Priority)
		default:
			fmt.Fprintf(&buf,
				"network={\n    ssid=%q\n    key_mgmt=WPA-PSK SAE\n    psk=%q\n    ieee80211w=1\n    priority=%d\n}\n\n",
				n.Ssid, n.Password, n.Priority)
		}
	}
	return os.WriteFile(wpaConfPath, []byte(buf.String()), 0600)
}

// ── Network status helpers ───────────────────────────────────────────────

func getNetworkStatus() *openrigv1.NetworkStatus {
	ns := &openrigv1.NetworkStatus{}

	iface := findDefaultRouteIface()

	if iface == "" {
		if isHostapdRunning() {
			ns.Mode = "ap"
			ns.Interface = "wlan0"
			ns.Ip = getIfaceIP("wlan0")
			ns.Connected = ns.Ip != ""
		} else {
			ns.Mode = "none"
		}
		return ns
	}

	ns.Interface = iface
	ns.Ip = getIfaceIP(iface)
	ns.Connected = ns.Ip != ""

	if strings.HasPrefix(iface, "wlan") || strings.HasPrefix(iface, "wifi") {
		ns.Mode = "wifi"
		ns.Ssid = getWifiSSID(iface)
		ns.SignalDbm = int32(getWifiSignalDBM(iface))
	} else if strings.HasPrefix(iface, "eth") {
		ns.Mode = "ethernet"
	} else {
		ns.Mode = "wifi"
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
		if fields[1] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil {
			continue
		}
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
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "inet" && i+1 < len(fields) {
				addr := fields[i+1]
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
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil {
			continue
		}
		if strings.Contains(string(data), "hostapd") {
			return true
		}
	}
	return false
}

// ── Config builders (JSON config → proto messages) ───────────────────────

func buildDeviceStatus(cfg map[string]any, m systemMetrics) *openrigv1.DeviceStatus {
	return &openrigv1.DeviceStatus{
		Provisioned: getBool(cfg, "openrig.device.provisioned"),
		DeviceType:  getString(cfg, "openrig.device.type", "unconfigured"),
		Callsign:    getString(cfg, "openrig.operator.callsign", ""),
		Version:     getString(cfg, "openrig.version", "0.1.0"),
		Hostname:    getString(cfg, "openrig.device.hostname", "openrig-config"),
		Uptime:      int32(m.Uptime),
		CpuPercent:  m.CPUPercent,
		MemTotalMb:  int32(m.MemTotalMB),
		MemUsedMb:   int32(m.MemUsedMB),
		DiskTotalGb: m.DiskTotalGB,
		DiskUsedGb:  m.DiskUsedGB,
	}
}

func buildDeviceConfig(cfg map[string]any) *openrigv1.DeviceConfig {
	return &openrigv1.DeviceConfig{
		Callsign:   getString(cfg, "openrig.operator.callsign", ""),
		Hostname:   getString(cfg, "openrig.device.hostname", ""),
		Timezone:   getString(cfg, "openrig.device.timezone", "UTC"),
		Name:       getString(cfg, "openrig.operator.name", ""),
		GridSquare: getString(cfg, "openrig.operator.grid_square", ""),
	}
}

func buildHotspotConfig(cfg map[string]any) *openrigv1.HotspotConfig {
	var tgs []*openrigv1.Talkgroup
	if raw, ok := nestedGet(cfg, "openrig.hotspot.dmr.talkgroups").([]any); ok {
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				tg := &openrigv1.Talkgroup{}
				if v, ok := m["tg"].(float64); ok {
					tg.Tg = int32(v)
				}
				if v, ok := m["slot"].(float64); ok {
					tg.Slot = int32(v)
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

	return &openrigv1.HotspotConfig{
		Dmr: &openrigv1.DMRConfig{
			Enabled:    getBool(cfg, "openrig.hotspot.dmr.enabled"),
			DmrId:      int32(getFloat(cfg, "openrig.hotspot.dmr.dmr_id")),
			Colorcode:  int32(getFloat(cfg, "openrig.hotspot.dmr.colorcode")),
			Network:    network,
			Server:     server,
			Password:   getString(cfg, "openrig.hotspot.dmr.password", ""),
			Talkgroups: tgs,
		},
		Ysf: &openrigv1.YSFConfig{
			Enabled:     getBool(cfg, "openrig.hotspot.ysf.enabled"),
			Network:     getString(cfg, "openrig.hotspot.ysf.network", "ysf"),
			Reflector:   getString(cfg, "openrig.hotspot.ysf.reflector", "AMERICA"),
			Module:      getString(cfg, "openrig.hotspot.ysf.module", ""),
			Suffix:      getString(cfg, "openrig.hotspot.ysf.suffix", ""),
			Description: getString(cfg, "openrig.hotspot.ysf.description", ""),
		},
		CrossMode: &openrigv1.CrossModeConfig{
			Ysf2DmrEnabled:   getBool(cfg, "openrig.hotspot.cross_mode.ysf2dmr_enabled"),
			Ysf2DmrTalkgroup: int32(getFloat(cfg, "openrig.hotspot.cross_mode.ysf2dmr_talkgroup")),
			Dmr2YsfEnabled:   getBool(cfg, "openrig.hotspot.cross_mode.dmr2ysf_enabled"),
			Dmr2YsfRoom:      getString(cfg, "openrig.hotspot.cross_mode.dmr2ysf_room", ""),
		},
		Modem: &openrigv1.ModemConfig{
			Type: getString(cfg, "openrig.hotspot.modem.type", "mmdvm_hs_hat"),
			Port: getString(cfg, "openrig.hotspot.modem.port", "/dev/ttyAMA0"),
		},
		RfFrequency: getFloat(cfg, "openrig.hotspot.rf_frequency"),
		TxFrequency: getFloat(cfg, "openrig.hotspot.tx_frequency"),
	}
}

func buildWifiConfig(cfg map[string]any) *openrigv1.WifiConfig {
	var networks []*openrigv1.WifiNetwork
	raw, ok := nestedGet(cfg, "openrig.network.wifi.networks").([]any)
	if !ok {
		return &openrigv1.WifiConfig{Networks: networks}
	}
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			n := &openrigv1.WifiNetwork{}
			if v, ok := m["ssid"].(string); ok {
				n.Ssid = v
			}
			if v, ok := m["security"].(string); ok {
				n.Security = v
			}
			if v, ok := m["priority"].(float64); ok {
				n.Priority = int32(v)
			}
			networks = append(networks, n)
		}
	}
	return &openrigv1.WifiConfig{Networks: networks}
}

func buildRigList(cfg map[string]any) *openrigv1.RigList {
	var rigs []*openrigv1.RigConfig
	raw, ok := nestedGet(cfg, "openrig.radio.rigs").([]any)
	if !ok {
		return &openrigv1.RigList{Rigs: rigs}
	}
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		rig := &openrigv1.RigConfig{
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
			rig.HamlibModelId = int32(v)
		}
		if v, ok := m["port"].(string); ok && v != "" {
			rig.Port = v
		}
		if v, ok := m["baud"].(float64); ok && v > 0 {
			rig.Baud = int32(v)
		}
		if v, ok := m["data_bits"].(float64); ok && v > 0 {
			rig.DataBits = int32(v)
		}
		if v, ok := m["stop_bits"].(float64); ok && v > 0 {
			rig.StopBits = int32(v)
		}
		if v, ok := m["parity"].(string); ok && v != "" {
			rig.Parity = v
		}
		if v, ok := m["handshake"].(string); ok && v != "" {
			rig.Handshake = v
		}
		rigs = append(rigs, rig)
	}
	return &openrigv1.RigList{Rigs: rigs}
}

// ── Last-heard parser ────────────────────────────────────────────────────

var (
	reMMDVMEnd = regexp.MustCompile(
		`^M: (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\.\d+ ` +
			`(DMR|YSF).*end of.*transmission from ([A-Z0-9/]+) to (?:TG )?(\S+).*?(\d+(?:\.\d+)?) seconds`)
)

func parseMMDVMLastHeard() []*openrigv1.LastHeardEntry {
	matches, err := filepath.Glob("/var/log/mmdvmhost/MMDVM-*.log")
	if err != nil || len(matches) == 0 {
		return nil
	}
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	logFile := matches[0]

	data, err := os.ReadFile(logFile)
	if err != nil {
		return nil
	}

	lines := strings.Split(string(data), "\n")
	start := len(lines) - 500
	if start < 0 {
		start = 0
	}
	lines = lines[start:]

	var entries []*openrigv1.LastHeardEntry
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
		entries = append(entries, &openrigv1.LastHeardEntry{
			Callsign:  m[3],
			Mode:      m[2],
			Info:      m[4],
			Duration:  fmt.Sprintf("%.0fs", secs),
			Timestamp: ts.Format(time.RFC3339),
		})
	}
	return entries
}

func devModeLastHeard() []*openrigv1.LastHeardEntry {
	now := time.Now()
	return []*openrigv1.LastHeardEntry{
		{Callsign: "W1ABC", Mode: "DMR", Info: "3100", Duration: "13s", Timestamp: now.Add(-2 * time.Minute).Format(time.RFC3339)},
		{Callsign: "K5XYZ", Mode: "YSF", Info: "AMERICA", Duration: "8s", Timestamp: now.Add(-5 * time.Minute).Format(time.RFC3339)},
		{Callsign: "N3DEF", Mode: "DMR", Info: "91", Duration: "22s", Timestamp: now.Add(-11 * time.Minute).Format(time.RFC3339)},
		{Callsign: "VE3RST", Mode: "DMR", Info: "302", Duration: "5s", Timestamp: now.Add(-18 * time.Minute).Format(time.RFC3339)},
		{Callsign: "JA1QRS", Mode: "YSF", Info: "FCS001-A", Duration: "15s", Timestamp: now.Add(-30 * time.Minute).Format(time.RFC3339)},
	}
}

// ── DMR server list helpers ──────────────────────────────────────────────

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
	"tgif":    {"tgif.network"},
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

func fetchHostsFile(url string, fallback []string) []string {
	client := &http.Client{Timeout: 2500 * time.Millisecond}
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
	client := &http.Client{Timeout: 2500 * time.Millisecond}
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

func getServersForNetwork(network string) []string {
	// Cache hit — return immediately (cache TTL is 1 hour)
	if servers, ok := getCachedServers(network); ok {
		return servers
	}

	// Cache miss — fetch live with a 3s timeout, fall back to hardcoded
	done := make(chan []string, 1)
	go func() {
		var live []string
		switch network {
		case "brandmeister":
			live = fetchBrandmeisterServers()
		case "ysf":
			live = fetchHostsFile("https://www.pistar.uk/downloads/YSFHosts.txt", hardcodedServers["ysf"])
		case "fcs":
			live = fetchHostsFile("https://www.pistar.uk/downloads/FCSHosts.txt", hardcodedServers["fcs"])
		default:
			done <- hardcodedServers[network]
			return
		}
		setCachedServers(network, live)
		done <- live
	}()

	select {
	case servers := <-done:
		return servers
	case <-time.After(3 * time.Second):
		fallback := hardcodedServers[network]
		if fallback == nil {
			return []string{}
		}
		return fallback
	}
}

// ══════════════════════════════════════════════════════════════════════════
// ConnectRPC service implementations
// ══════════════════════════════════════════════════════════════════════════

// ── DeviceService ────────────────────────────────────────────────────────

type deviceServer struct {
	openrigv1connect.UnimplementedDeviceServiceHandler
}

func (s *deviceServer) GetStatus(_ context.Context, _ *connect.Request[openrigv1.Empty]) (*connect.Response[openrigv1.DeviceStatus], error) {
	cfg, err := readConfig()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cannot read config"))
	}
	m := getSystemMetrics()
	return connect.NewResponse(buildDeviceStatus(cfg, m)), nil
}

func (s *deviceServer) StreamStatus(ctx context.Context, _ *connect.Request[openrigv1.Empty], stream *connect.ServerStream[openrigv1.DeviceStatus]) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Send one immediately
	cfg, err := readConfig()
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("cannot read config"))
	}
	m := getSystemMetrics()
	if err := stream.Send(buildDeviceStatus(cfg, m)); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			cfg, err := readConfig()
			if err != nil {
				continue
			}
			m := getSystemMetrics()
			if err := stream.Send(buildDeviceStatus(cfg, m)); err != nil {
				return err
			}
		}
	}
}

func (s *deviceServer) GetConfig(_ context.Context, _ *connect.Request[openrigv1.Empty]) (*connect.Response[openrigv1.DeviceConfig], error) {
	cfg, err := readConfig()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cannot read config"))
	}
	return connect.NewResponse(buildDeviceConfig(cfg)), nil
}

func (s *deviceServer) UpdateConfig(_ context.Context, req *connect.Request[openrigv1.UpdateConfigRequest]) (*connect.Response[openrigv1.DeviceConfig], error) {
	cfg, err := readConfig()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cannot read config"))
	}

	c := req.Msg.Config
	if c == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("config is required"))
	}

	if c.Callsign != "" {
		nested(cfg, "openrig.operator.callsign", strings.ToUpper(c.Callsign))
	}
	if c.Hostname != "" {
		re := regexp.MustCompile(`[^a-z0-9-]`)
		hostname := re.ReplaceAllString(strings.ToLower(c.Hostname), "")
		nested(cfg, "openrig.device.hostname", hostname)
		if !devMode {
			exec.Command("hostnamectl", "set-hostname", hostname).Run()
		}
	}
	if c.Timezone != "" {
		nested(cfg, "openrig.device.timezone", c.Timezone)
		if !devMode {
			exec.Command("timedatectl", "set-timezone", c.Timezone).Run()
		}
	}
	if c.Name != "" {
		nested(cfg, "openrig.operator.name", c.Name)
	}
	if c.GridSquare != "" {
		nested(cfg, "openrig.operator.grid_square", strings.ToUpper(c.GridSquare))
	}

	if err := writeConfig(cfg); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cannot write config"))
	}

	return connect.NewResponse(buildDeviceConfig(cfg)), nil
}

func (s *deviceServer) RestartService(_ context.Context, req *connect.Request[openrigv1.RestartServiceRequest]) (*connect.Response[openrigv1.RestartServiceResponse], error) {
	name := req.Msg.Service
	if _, ok := validServices[name]; !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown service: %s (valid: dmr, ysf, ysf2dmr, dmr2ysf, wifi, mmdvmhost, rigctld, dmrgateway, ysfgateway)", name))
	}
	if err := doRestartService(name); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("restart failed: %v", err))
	}
	return connect.NewResponse(&openrigv1.RestartServiceResponse{}), nil
}

func (s *deviceServer) Reboot(_ context.Context, _ *connect.Request[openrigv1.RebootRequest]) (*connect.Response[openrigv1.RebootResponse], error) {
	if !devMode {
		go func() {
			time.Sleep(500 * time.Millisecond)
			exec.Command("systemctl", "reboot").Run()
		}()
	} else {
		log.Println("[dev] reboot skipped")
	}
	return connect.NewResponse(&openrigv1.RebootResponse{}), nil
}

func (s *deviceServer) Shutdown(_ context.Context, _ *connect.Request[openrigv1.ShutdownRequest]) (*connect.Response[openrigv1.ShutdownResponse], error) {
	if !devMode {
		go func() {
			time.Sleep(500 * time.Millisecond)
			exec.Command("systemctl", "poweroff").Run()
		}()
	} else {
		log.Println("[dev] poweroff skipped")
	}
	return connect.NewResponse(&openrigv1.ShutdownResponse{}), nil
}

// ── HotspotService ───────────────────────────────────────────────────────

type hotspotServer struct {
	openrigv1connect.UnimplementedHotspotServiceHandler
}

func (s *hotspotServer) GetHotspot(_ context.Context, _ *connect.Request[openrigv1.Empty]) (*connect.Response[openrigv1.HotspotConfig], error) {
	cfg, err := readConfig()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cannot read config"))
	}
	return connect.NewResponse(buildHotspotConfig(cfg)), nil
}

func (s *hotspotServer) UpdateHotspot(_ context.Context, req *connect.Request[openrigv1.UpdateHotspotRequest]) (*connect.Response[openrigv1.HotspotConfig], error) {
	cfg, err := readConfig()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cannot read config"))
	}

	hc := req.Msg.Config
	if hc == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("config is required"))
	}

	// Validate
	if hc.Dmr != nil {
		if hc.Dmr.Colorcode < 1 || hc.Dmr.Colorcode > 15 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("colorcode must be 1-15"))
		}
		if hc.Dmr.Network != "" && !validDMRNetworks[hc.Dmr.Network] {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid dmr network %q (valid: brandmeister, dmrplus, freedmr, tgif, systemx, xlx, custom)", hc.Dmr.Network))
		}

		nested(cfg, "openrig.hotspot.dmr.enabled", hc.Dmr.Enabled)
		nested(cfg, "openrig.hotspot.dmr.dmr_id", int(hc.Dmr.DmrId))
		nested(cfg, "openrig.hotspot.dmr.colorcode", int(hc.Dmr.Colorcode))
		nested(cfg, "openrig.hotspot.dmr.network", hc.Dmr.Network)
		nested(cfg, "openrig.hotspot.dmr.server", hc.Dmr.Server)
		nested(cfg, "openrig.hotspot.dmr.password", hc.Dmr.Password)

		tgs := make([]any, len(hc.Dmr.Talkgroups))
		for i, tg := range hc.Dmr.Talkgroups {
			tgs[i] = map[string]any{"tg": int(tg.Tg), "slot": int(tg.Slot), "name": tg.Name}
		}
		nested(cfg, "openrig.hotspot.dmr.talkgroups", tgs)
	}

	if hc.Ysf != nil {
		if hc.Ysf.Network != "" && !validYSFNetworks[hc.Ysf.Network] {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid ysf network %q (valid: ysf, fcs, custom)", hc.Ysf.Network))
		}

		nested(cfg, "openrig.hotspot.ysf.enabled", hc.Ysf.Enabled)
		nested(cfg, "openrig.hotspot.ysf.network", hc.Ysf.Network)
		nested(cfg, "openrig.hotspot.ysf.reflector", hc.Ysf.Reflector)
		nested(cfg, "openrig.hotspot.ysf.module", hc.Ysf.Module)
		nested(cfg, "openrig.hotspot.ysf.suffix", hc.Ysf.Suffix)
		nested(cfg, "openrig.hotspot.ysf.description", hc.Ysf.Description)
	}

	if hc.CrossMode != nil {
		nested(cfg, "openrig.hotspot.cross_mode.ysf2dmr_enabled", hc.CrossMode.Ysf2DmrEnabled)
		nested(cfg, "openrig.hotspot.cross_mode.ysf2dmr_talkgroup", int(hc.CrossMode.Ysf2DmrTalkgroup))
		nested(cfg, "openrig.hotspot.cross_mode.dmr2ysf_enabled", hc.CrossMode.Dmr2YsfEnabled)
		nested(cfg, "openrig.hotspot.cross_mode.dmr2ysf_room", hc.CrossMode.Dmr2YsfRoom)
	}

	nested(cfg, "openrig.hotspot.rf_frequency", hc.RfFrequency)
	nested(cfg, "openrig.hotspot.tx_frequency", hc.TxFrequency)

	if hc.Modem != nil {
		nested(cfg, "openrig.hotspot.modem.type", hc.Modem.Type)
		nested(cfg, "openrig.hotspot.modem.port", hc.Modem.Port)
	}

	if err := writeConfig(cfg); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cannot write config"))
	}

	// Update MMDVM configs and restart affected services
	go func() {
		if !devMode {
			exec.Command("/usr/local/lib/openrig/mmdvm-update.sh").Run()
		}
		if hc.Dmr != nil && hc.Dmr.Enabled {
			doRestartService("dmr")
			doRestartService("dmrgateway")
		}
		if hc.Ysf != nil && hc.Ysf.Enabled {
			doRestartService("ysf")
			doRestartService("ysfgateway")
		}
		if hc.CrossMode != nil {
			if hc.CrossMode.Ysf2DmrEnabled {
				doRestartService("ysf2dmr")
			}
			if hc.CrossMode.Dmr2YsfEnabled {
				doRestartService("dmr2ysf")
			}
		}
	}()

	return connect.NewResponse(buildHotspotConfig(cfg)), nil
}

func (s *hotspotServer) UpdateDmrId(_ context.Context, req *connect.Request[openrigv1.UpdateDmrIdRequest]) (*connect.Response[openrigv1.UpdateDmrIdResponse], error) {
	cfg, err := readConfig()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cannot read config"))
	}

	dmrID := int(req.Msg.DmrId)
	if dmrID < 1000000 || dmrID > 9999999 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("dmr_id must be a 7-digit number (1000000-9999999)"))
	}

	nested(cfg, "openrig.operator.dmr_id", dmrID)
	if err := writeConfig(cfg); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cannot write config"))
	}

	if !devMode {
		go exec.Command("/usr/local/lib/openrig/mmdvm-update.sh").Run()
	}

	return connect.NewResponse(&openrigv1.UpdateDmrIdResponse{}), nil
}

func (s *hotspotServer) GetServers(_ context.Context, req *connect.Request[openrigv1.GetServersRequest]) (*connect.Response[openrigv1.GetServersResponse], error) {
	servers := getServersForNetwork(req.Msg.Network)
	return connect.NewResponse(&openrigv1.GetServersResponse{Servers: servers}), nil
}

func (s *hotspotServer) StreamLastHeard(ctx context.Context, _ *connect.Request[openrigv1.Empty], stream *connect.ServerStream[openrigv1.LastHeardEntry]) error {
	if devMode {
		entries := devModeLastHeard()
		for _, e := range entries {
			if err := stream.Send(e); err != nil {
				return err
			}
		}
		// Block until client disconnects
		<-ctx.Done()
		return nil
	}

	// Track already-sent entries by timestamp to avoid duplicates
	sent := make(map[string]bool)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Send initial batch
	entries := parseMMDVMLastHeard()
	for _, e := range entries {
		sent[e.Timestamp+e.Callsign] = true
		if err := stream.Send(e); err != nil {
			return err
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			entries := parseMMDVMLastHeard()
			for _, e := range entries {
				key := e.Timestamp + e.Callsign
				if sent[key] {
					continue
				}
				sent[key] = true
				if err := stream.Send(e); err != nil {
					return err
				}
			}
		}
	}
}

// ── WifiService ──────────────────────────────────────────────────────────

type wifiServer struct {
	openrigv1connect.UnimplementedWifiServiceHandler
}

func (s *wifiServer) GetWifi(_ context.Context, _ *connect.Request[openrigv1.Empty]) (*connect.Response[openrigv1.WifiConfig], error) {
	cfg, err := readConfig()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cannot read config"))
	}
	return connect.NewResponse(buildWifiConfig(cfg)), nil
}

func (s *wifiServer) UpdateWifi(_ context.Context, req *connect.Request[openrigv1.UpdateWifiRequest]) (*connect.Response[openrigv1.WifiConfig], error) {
	cfg, err := readConfig()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cannot read config"))
	}

	wc := req.Msg.Config
	if wc == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("config is required"))
	}

	for _, n := range wc.Networks {
		if n.Password != "" && len(n.Password) < 8 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("WiFi password for %q must be at least 8 characters", n.Ssid))
		}
	}

	// Store networks in config (without passwords)
	netList := make([]any, len(wc.Networks))
	for i, n := range wc.Networks {
		netList[i] = map[string]any{
			"ssid":     n.Ssid,
			"security": n.Security,
			"priority": int(n.Priority),
		}
	}
	nested(cfg, "openrig.network.wifi.networks", netList)
	if err := writeConfig(cfg); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cannot write config"))
	}

	country := getString(cfg, "openrig.network.wifi.country", "US")
	if err := writeWPAConf(wc.Networks, country); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cannot write WiFi config"))
	}

	go doRestartService("wifi")

	return connect.NewResponse(buildWifiConfig(cfg)), nil
}

func (s *wifiServer) ScanWifi(_ context.Context, _ *connect.Request[openrigv1.Empty]) (*connect.Response[openrigv1.ScanWifiResponse], error) {
	out, err := exec.Command("nmcli", "-t", "-f", "SSID,SIGNAL,SECURITY", "dev", "wifi", "list").CombinedOutput()
	if err != nil {
		return connect.NewResponse(&openrigv1.ScanWifiResponse{}), nil
	}

	type scanResult struct {
		ssid     string
		signal   int
		security string
	}
	best := make(map[string]scanResult)
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
		if existing, ok := best[ssid]; !ok || signal > existing.signal {
			best[ssid] = scanResult{ssid: ssid, signal: signal, security: security}
		}
	}

	var networks []*openrigv1.ScannedNetwork
	for _, n := range best {
		networks = append(networks, &openrigv1.ScannedNetwork{
			Ssid:      n.ssid,
			SignalDbm: int32(n.signal),
			Security:  n.security,
		})
	}
	sort.Slice(networks, func(i, j int) bool {
		return networks[i].SignalDbm > networks[j].SignalDbm
	})

	return connect.NewResponse(&openrigv1.ScanWifiResponse{Networks: networks}), nil
}

func (s *wifiServer) GetNetwork(_ context.Context, _ *connect.Request[openrigv1.Empty]) (*connect.Response[openrigv1.NetworkStatus], error) {
	return connect.NewResponse(getNetworkStatus()), nil
}

// ── RigService ───────────────────────────────────────────────────────────

type rigServer struct {
	openrigv1connect.UnimplementedRigServiceHandler
}

func (s *rigServer) GetRigs(_ context.Context, _ *connect.Request[openrigv1.Empty]) (*connect.Response[openrigv1.RigList], error) {
	cfg, err := readConfig()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cannot read config"))
	}
	return connect.NewResponse(buildRigList(cfg)), nil
}

func (s *rigServer) UpdateRigs(_ context.Context, req *connect.Request[openrigv1.UpdateRigsRequest]) (*connect.Response[openrigv1.RigList], error) {
	cfg, err := readConfig()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cannot read config"))
	}

	for i, rig := range req.Msg.Rigs {
		if rig.HamlibModelId < 1 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("rig[%d]: hamlib_model_id must be a positive integer", i))
		}
		if !validBaudRates[int(rig.Baud)] {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("rig[%d]: baud must be one of 1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200", i))
		}
		if !validParities[rig.Parity] {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("rig[%d]: parity must be none, even, or odd", i))
		}
		if !validHandshakes[rig.Handshake] {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("rig[%d]: handshake must be none, hardware, or software", i))
		}
	}

	rigList := make([]any, len(req.Msg.Rigs))
	for i, rig := range req.Msg.Rigs {
		rigList[i] = map[string]any{
			"id":              fmt.Sprintf("rig%d", i+1),
			"enabled":         rig.Enabled,
			"hamlib_model_id": int(rig.HamlibModelId),
			"port":            rig.Port,
			"baud":            int(rig.Baud),
			"data_bits":       int(rig.DataBits),
			"stop_bits":       int(rig.StopBits),
			"parity":          rig.Parity,
			"handshake":       rig.Handshake,
		}
	}
	nested(cfg, "openrig.radio.rigs", rigList)

	if err := writeConfig(cfg); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cannot write config"))
	}

	go doRestartService("rigctld")

	return connect.NewResponse(buildRigList(cfg)), nil
}

// ── Main ─────────────────────────────────────────────────────────────────

func main() {
	flag.BoolVar(&devMode, "dev", false, "Run in local dev mode: use ./openrig.json, stub system calls")
	flag.Parse()

	if devMode {
		configPath = "./openrig.json"
		wpaConfPath = "./wpa_supplicant-dev.conf"
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

	// Register ConnectRPC service handlers
	path, handler := openrigv1connect.NewDeviceServiceHandler(&deviceServer{})
	mux.Handle(path, handler)

	path, handler = openrigv1connect.NewHotspotServiceHandler(&hotspotServer{})
	mux.Handle(path, handler)

	path, handler = openrigv1connect.NewWifiServiceHandler(&wifiServer{})
	mux.Handle(path, handler)

	path, handler = openrigv1connect.NewRigServiceHandler(&rigServer{})
	mux.Handle(path, handler)

	// gRPC server reflection — enables grpcurl and buf curl schema discovery
	reflector := grpcreflect.NewStaticReflector(
		"openrig.v1.DeviceService",
		"openrig.v1.HotspotService",
		"openrig.v1.WifiService",
		"openrig.v1.RigService",
	)
	mux.Handle(grpcreflect.NewHandlerV1(reflector))
	mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))

	log.Printf("openRig API listening on %s", listenAddr)
	if err := http.ListenAndServe(listenAddr, h2c.NewHandler(mux, &http2.Server{})); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
