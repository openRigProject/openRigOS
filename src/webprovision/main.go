// openRigOS Web UI
//
// Single static binary — no runtime dependencies.
// Serves on port 80 permanently.
// Pre-provisioning: serves the provisioning wizard.
// Post-provisioning: redirects to /management for settings.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	configPath  = "/etc/openrig.json"
	wpaConfPath = "/etc/wpa_supplicant/wpa_supplicant.conf"
	listenAddr  = ":80"
	devMode     bool
)

// ── Reference data ────────────────────────────────────────────────────────

type option struct{ Value, Label string }

var deviceTypes = []option{
	{"hotspot", "Hotspot — cross-mode digital repeater"},
	{"rigctl", "Rig Control — remote radio operation"},
	{"console", "Console — station workstation"},
}

var countries = []option{
	{"US", "United States"}, {"CA", "Canada"}, {"GB", "United Kingdom"},
	{"AU", "Australia"}, {"DE", "Germany"}, {"FR", "France"},
	{"JP", "Japan"}, {"BR", "Brazil"}, {"MX", "Mexico"},
	{"NZ", "New Zealand"}, {"ZA", "South Africa"}, {"IN", "India"},
	{"AR", "Argentina"}, {"IT", "Italy"}, {"ES", "Spain"},
	{"NL", "Netherlands"}, {"SE", "Sweden"}, {"NO", "Norway"},
	{"FI", "Finland"}, {"DK", "Denmark"},
}

var timezones = []option{
	{"UTC", "UTC"},
	{"America/New_York", "US Eastern"},
	{"America/Chicago", "US Central"},
	{"America/Denver", "US Mountain"},
	{"America/Los_Angeles", "US Pacific"},
	{"America/Anchorage", "US Alaska"},
	{"Pacific/Honolulu", "US Hawaii"},
	{"America/Toronto", "Canada Eastern"},
	{"America/Vancouver", "Canada Pacific"},
	{"America/Sao_Paulo", "Brazil"},
	{"America/Mexico_City", "Mexico"},
	{"America/Argentina/Buenos_Aires", "Argentina"},
	{"Europe/London", "UK"},
	{"Europe/Dublin", "Ireland"},
	{"Europe/Paris", "France / Central Europe"},
	{"Europe/Berlin", "Germany"},
	{"Europe/Amsterdam", "Netherlands"},
	{"Europe/Stockholm", "Sweden"},
	{"Europe/Oslo", "Norway"},
	{"Europe/Helsinki", "Finland"},
	{"Europe/Rome", "Italy"},
	{"Europe/Madrid", "Spain"},
	{"Asia/Tokyo", "Japan"},
	{"Asia/Shanghai", "China"},
	{"Asia/Kolkata", "India"},
	{"Asia/Dubai", "UAE"},
	{"Australia/Sydney", "Australia Eastern"},
	{"Australia/Perth", "Australia Western"},
	{"Pacific/Auckland", "New Zealand"},
	{"Africa/Johannesburg", "South Africa"},
	{"Africa/Cairo", "Egypt"},
}

var securityModes = []option{
	{"auto", "WPA2 + WPA3 (recommended)"},
	{"wpa2", "WPA2 only"},
	{"wpa3", "WPA3 only"},
}

// ── Config ────────────────────────────────────────────────────────────────

func readConfig() (map[string]any, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var cfg map[string]any
	return cfg, json.Unmarshal(data, &cfg)
}

func writeConfig(cfg map[string]any) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0644)
}

// nested navigates a dot-separated path and sets the leaf value.
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

func isProvisioned(cfg map[string]any) bool {
	openrig, ok := cfg["openrig"].(map[string]any)
	if !ok {
		return false
	}
	device, ok := openrig["device"].(map[string]any)
	if !ok {
		return false
	}
	provisioned, _ := device["provisioned"].(bool)
	return provisioned
}

// ── WiFi scanning ─────────────────────────────────────────────────────────

func scanNetworks() ([]string, error) {
	if devMode {
		return []string{"HomeNetwork", "OfficeWiFi", "HamShack-5G", "Neighbor_2G"}, nil
	}
	// Find the first wireless interface
	iface := "wlan0"
	out, err := exec.Command("iw", "dev", iface, "scan", "ap-force").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("scan failed: %w", err)
	}
	seen := make(map[string]bool)
	var ssids []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "SSID: ") {
			ssid := strings.TrimPrefix(line, "SSID: ")
			if ssid != "" && !seen[ssid] {
				seen[ssid] = true
				ssids = append(ssids, ssid)
			}
		}
	}
	sort.Strings(ssids)
	return ssids, nil
}

// ── System operations ─────────────────────────────────────────────────────

func changePassword(password string) error {
	if devMode {
		log.Printf("[dev] changePassword(%q) skipped", password)
		return nil
	}
	cmd := exec.Command("chpasswd")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("openrig:%s", password))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	// Clear the forced-expiry flag set at build time
	return exec.Command("chage", "-d", "-1", "openrig").Run()
}

func applyHostname(hostname string) error {
	if devMode {
		log.Printf("[dev] applyHostname(%q) skipped", hostname)
		return nil
	}
	if err := exec.Command("hostnamectl", "set-hostname", hostname).Run(); err != nil {
		return err
	}
	data, err := os.ReadFile("/etc/hosts")
	if err != nil {
		return err
	}
	re := regexp.MustCompile(`(?m)^127\.0\.1\.1\s+.*$`)
	updated := re.ReplaceAll(data, []byte("127.0.1.1   "+hostname))
	if !re.Match(data) {
		updated = append(data, []byte("\n127.0.1.1   "+hostname+"\n")...)
	}
	if err := os.WriteFile("/etc/hosts", updated, 0644); err != nil {
		return err
	}
	exec.Command("systemctl", "restart", "avahi-daemon").Run()
	return nil
}

func applyTimezone(tz string) error {
	if devMode {
		log.Printf("[dev] applyTimezone(%q) skipped", tz)
		return nil
	}
	return exec.Command("timedatectl", "set-timezone", tz).Run()
}

// wifiNetwork holds config for one network entry.
type wifiNetwork struct {
	SSID     string
	Password string
	Security string
	Priority int
}

func writeWPAConf(networks []wifiNetwork, country string) error {
	if devMode {
		log.Printf("[dev] writeWPAConf(%d networks, country=%q) skipped", len(networks), country)
		return nil
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, "country=%s\nctrl_interface=DIR=/var/run/wpa_supplicant GROUP=netdev\nupdate_config=1\n\n", country)
	for _, n := range networks {
		switch n.Security {
		case "wpa3":
			fmt.Fprintf(&buf,
				"network={\n    ssid=%q\n    key_mgmt=SAE\n    psk=%q\n    ieee80211w=2\n    priority=%d\n}\n\n",
				n.SSID, n.Password, n.Priority)
		case "wpa2":
			fmt.Fprintf(&buf,
				"network={\n    ssid=%q\n    key_mgmt=WPA-PSK\n    psk=%q\n    priority=%d\n}\n\n",
				n.SSID, n.Password, n.Priority)
		default: // auto — WPA2+WPA3 transition
			fmt.Fprintf(&buf,
				"network={\n    ssid=%q\n    key_mgmt=WPA-PSK SAE\n    psk=%q\n    ieee80211w=1\n    priority=%d\n}\n\n",
				n.SSID, n.Password, n.Priority)
		}
	}
	return os.WriteFile(wpaConfPath, []byte(buf.String()), 0600)
}

func generateHostname(callsign, deviceType string) string {
	re := regexp.MustCompile(`[^a-z0-9]`)
	slug := re.ReplaceAllString(strings.ToLower(callsign), "")
	return slug + "-" + deviceType
}

// ── Branding assets ───────────────────────────────────────────────────────

const faviconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="-10 -10 220 220" fill="none"><style>.r{stroke:#38bdf8;stroke-width:4;fill:none;stroke-linecap:round;stroke-linejoin:round}</style><rect class="r" x="12" y="52" width="176" height="120" rx="9"/><path class="r" d="M62 52Q62 32 100 32Q138 32 138 52"/><rect class="r" x="22" y="167" width="22" height="11" rx="3"/><rect class="r" x="156" y="167" width="22" height="11" rx="3"/><rect class="r" x="15" y="40" width="20" height="15" rx="2.5"/><line class="r" x1="16" y1="41" x2="58" y2="-8"/><line class="r" x1="28" y1="45" x2="70" y2="-4"/><circle class="r" cx="64" cy="-8" r="5"/><circle class="r" cx="72" cy="114" r="46"/><circle class="r" cx="72" cy="114" r="33"/><circle class="r" cx="72" cy="114" r="19"/><line class="r" x1="72" y1="68" x2="72" y2="95"/><line class="r" x1="72" y1="133" x2="72" y2="160"/><line class="r" x1="26" y1="114" x2="53" y2="114"/><line class="r" x1="91" y1="114" x2="118" y2="114"/><circle class="r" cx="72" cy="114" r="7"/><rect class="r" x="128" y="64" width="48" height="30" rx="4"/><rect class="r" x="132" y="68" width="40" height="22" rx="2"/><rect class="r" x="128" y="101" width="14" height="10" rx="2"/><rect class="r" x="147" y="101" width="14" height="10" rx="2"/><rect class="r" x="166" y="101" width="14" height="10" rx="2"/><rect class="r" x="128" y="116" width="14" height="10" rx="2"/><rect class="r" x="147" y="116" width="14" height="10" rx="2"/><rect class="r" x="166" y="116" width="14" height="10" rx="2"/><circle class="r" cx="170" cy="148" r="16"/><circle class="r" cx="170" cy="148" r="9"/><line class="r" x1="170" y1="139" x2="170" y2="132"/></svg>`

const inlineLogoSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="-10 -10 220 220" width="28" height="28" fill="none" style="flex-shrink:0"><rect stroke="#38bdf8" stroke-width="5" stroke-linecap="round" stroke-linejoin="round" x="12" y="52" width="176" height="120" rx="9"/><path stroke="#38bdf8" stroke-width="5" fill="none" stroke-linecap="round" stroke-linejoin="round" d="M62 52Q62 32 100 32Q138 32 138 52"/><circle stroke="#38bdf8" stroke-width="5" fill="none" cx="72" cy="114" r="46"/><circle stroke="#38bdf8" stroke-width="5" fill="none" cx="72" cy="114" r="19"/><circle stroke="#38bdf8" stroke-width="5" fill="#38bdf8" cx="72" cy="114" r="7"/><rect stroke="#38bdf8" stroke-width="5" stroke-linecap="round" stroke-linejoin="round" x="128" y="64" width="48" height="30" rx="4"/><circle stroke="#38bdf8" stroke-width="5" fill="none" cx="170" cy="148" r="16"/></svg>`

// ── HTML template ─────────────────────────────────────────────────────────

var pageTmpl = template.Must(template.New("page").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>openRigOS — {{.Title}}</title>
<link rel="icon" type="image/svg+xml" href="/favicon.svg">
<style>
  *,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
  body{font-family:system-ui,-apple-system,sans-serif;background:#0f172a;color:#e2e8f0;
    min-height:100vh;display:flex;align-items:flex-start;justify-content:center;padding:1.5rem 1rem}
  .card{background:#1e293b;border:1px solid #334155;border-radius:12px;padding:2rem;width:100%;max-width:460px}
  .logo{font-size:1.4rem;font-weight:700;color:#38bdf8;margin-bottom:.2rem}
  .sub{color:#64748b;font-size:.875rem;margin-bottom:2rem}
  .section{border-top:1px solid #334155;padding-top:1.25rem;margin-top:1.25rem}
  .section:first-of-type{border-top:none;margin-top:0;padding-top:0}
  .stitle{font-size:.7rem;font-weight:700;text-transform:uppercase;letter-spacing:.08em;color:#64748b;margin-bottom:.85rem}
  label{display:block;font-size:.85rem;color:#94a3b8;margin-bottom:.25rem;margin-top:.85rem}
  label:first-of-type{margin-top:0}
  input,select{width:100%;background:#0f172a;border:1px solid #475569;border-radius:6px;
    padding:.6rem .75rem;color:#e2e8f0;font-size:1rem;outline:none;transition:border-color .15s}
  input:focus,select:focus{border-color:#38bdf8}
  .hint{font-size:.75rem;color:#64748b;margin-top:.3rem}
  .opt{color:#475569;font-weight:400}
  .error{background:#450a0a;border:1px solid #7f1d1d;color:#fca5a5;
    border-radius:6px;padding:.6rem .75rem;font-size:.875rem;margin-bottom:1rem}
  button[type=submit]{width:100%;margin-top:1.75rem;background:#0284c7;color:#fff;border:none;
    border-radius:6px;padding:.75rem;font-size:1rem;font-weight:600;cursor:pointer;transition:background .15s}
  button[type=submit]:hover{background:#0369a1}
  .btn-sm{background:#1e3a5f;color:#7dd3fc;border:1px solid #1e40af;border-radius:6px;
    padding:.4rem .7rem;font-size:.8rem;cursor:pointer;white-space:nowrap;transition:background .15s}
  .btn-sm:hover{background:#1e40af}
  .btn-danger{background:#450a0a;color:#fca5a5;border:1px solid #7f1d1d;border-radius:6px;
    padding:.3rem .6rem;font-size:.8rem;cursor:pointer;transition:background .15s}
  .btn-danger:hover{background:#7f1d1d}
  .btn-add{width:100%;margin-top:1rem;background:transparent;color:#38bdf8;border:1px dashed #334155;
    border-radius:6px;padding:.6rem;font-size:.875rem;cursor:pointer;transition:border-color .15s}
  .btn-add:hover{border-color:#38bdf8}
  .ssid-row{display:flex;gap:.5rem;align-items:flex-end}
  .ssid-row input{flex:1}
  .scan-list{background:#0f172a;border:1px solid #334155;border-radius:6px;
    margin-top:.4rem;max-height:160px;overflow-y:auto}
  .scan-item{padding:.5rem .75rem;font-size:.875rem;cursor:pointer;border-bottom:1px solid #1e293b;
    color:#cbd5e1;transition:background .1s}
  .scan-item:last-child{border-bottom:none}
  .scan-item:hover{background:#1e293b;color:#38bdf8}
  .scan-empty{padding:.5rem .75rem;font-size:.8rem;color:#475569}
  .net-block{border:1px solid #334155;border-radius:8px;padding:1rem;margin-top:1rem}
  .net-header{display:flex;justify-content:space-between;align-items:center;margin-bottom:.75rem}
  .net-label{font-size:.8rem;font-weight:600;color:#94a3b8}
  .done-icon{font-size:3rem;text-align:center;margin-bottom:1rem}
  .done-row{font-size:.875rem;color:#64748b;margin-top:.4rem;text-align:center}
  .done-row strong{color:#e2e8f0}
  .done-note{font-size:.8rem;color:#475569;text-align:center;margin-top:1.25rem}
</style>
</head>
<body><div class="card">
  <div style="display:flex;align-items:center;gap:.6rem;margin-bottom:.2rem"><svg xmlns="http://www.w3.org/2000/svg" viewBox="-10 -10 220 220" width="28" height="28" fill="none" style="flex-shrink:0"><rect stroke="#38bdf8" stroke-width="5" stroke-linecap="round" stroke-linejoin="round" x="12" y="52" width="176" height="120" rx="9"/><path stroke="#38bdf8" stroke-width="5" fill="none" stroke-linecap="round" stroke-linejoin="round" d="M62 52Q62 32 100 32Q138 32 138 52"/><circle stroke="#38bdf8" stroke-width="5" fill="none" cx="72" cy="114" r="46"/><circle stroke="#38bdf8" stroke-width="5" fill="none" cx="72" cy="114" r="19"/><circle stroke="#38bdf8" stroke-width="5" fill="#38bdf8" cx="72" cy="114" r="7"/><rect stroke="#38bdf8" stroke-width="5" stroke-linecap="round" stroke-linejoin="round" x="128" y="64" width="48" height="30" rx="4"/><circle stroke="#38bdf8" stroke-width="5" fill="none" cx="170" cy="148" r="16"/></svg><div class="logo">openRig<span style="color:#94a3b8;font-weight:500">OS</span></div></div>
  <div class="sub">{{.Subtitle}}</div>
  {{if .Error}}<div class="error">{{.Error}}</div>{{end}}
  {{.Body}}
</div>
<script>
// ── Hostname sync ────────────────────────────────────────────────────────
function syncHostname(){
  var cs=(document.querySelector('[name=callsign]')||{}).value||'';
  var t=(document.querySelector('[name=device_type]')||{}).value||'';
  var hf=document.querySelector('[name=hostname]');
  if(!hf||hf.dataset.manual==='true')return;
  if(cs&&t)hf.value=cs.toLowerCase().replace(/[^a-z0-9]/g,'')+'-'+t;
}
function syncDmrIdVisibility(){
  var dt=(document.querySelector('[name=device_type]')||{}).value||'';
  var sec=document.getElementById('dmrid-section');
  if(sec)sec.style.display=(dt==='hotspot')?'':'none';
}
document.addEventListener('DOMContentLoaded',function(){
  var cs=document.querySelector('[name=callsign]');
  var dt=document.querySelector('[name=device_type]');
  var hf=document.querySelector('[name=hostname]');
  if(cs)cs.addEventListener('input',syncHostname);
  if(dt){dt.addEventListener('change',syncHostname);dt.addEventListener('change',syncDmrIdVisibility);}
  if(hf)hf.addEventListener('input',function(){hf.dataset.manual=hf.value?'true':'false';});
  syncDmrIdVisibility();
});

// ── WiFi scan ────────────────────────────────────────────────────────────
function scanWifi(idx){
  var btn=document.getElementById('scan-btn-'+idx);
  var box=document.getElementById('scan-results-'+idx);
  if(btn){btn.textContent='Scanning…';btn.disabled=true;}
  fetch('/scan')
    .then(function(r){return r.json();})
    .then(function(data){
      if(btn){btn.textContent='Scan';btn.disabled=false;}
      if(!data.networks||data.networks.length===0){
        box.innerHTML='<div class="scan-empty">No networks found — try again or type manually.</div>';
      } else {
        var html='';
        data.networks.forEach(function(ssid){
          var safe=ssid.replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
          html+='<div class="scan-item" onclick="selectSSID('+idx+',this.dataset.ssid)" data-ssid="'+safe+'">'+safe+'</div>';
        });
        box.innerHTML=html;
      }
      box.style.display='block';
    })
    .catch(function(){
      if(btn){btn.textContent='Scan';btn.disabled=false;}
      box.innerHTML='<div class="scan-empty">Scan failed — enter SSID manually.</div>';
      box.style.display='block';
    });
}

function selectSSID(idx,ssid){
  var input=document.getElementById('ssid-input-'+idx);
  if(input)input.value=ssid;
  var box=document.getElementById('scan-results-'+idx);
  if(box)box.style.display='none';
}

// ── Additional networks ──────────────────────────────────────────────────
var netCount=1;
var maxNets=5;

function securityOptions(){
  return '<option value="auto">WPA2 + WPA3 (recommended)</option>'+
         '<option value="wpa2">WPA2 only</option>'+
         '<option value="wpa3">WPA3 only</option>';
}

function addNetwork(){
  if(netCount>=maxNets)return;
  var i=netCount;
  var div=document.createElement('div');
  div.className='net-block';
  div.id='net-block-'+i;
  div.innerHTML=
    '<div class="net-header">'+
      '<span class="net-label">Network '+(i+1)+'</span>'+
      '<button type="button" class="btn-danger" onclick="removeNetwork('+i+')">Remove</button>'+
    '</div>'+
    '<div class="ssid-row">'+
      '<input type="text" id="ssid-input-'+i+'" name="ssid_'+i+'" placeholder="Network name (SSID)">'+
      '<button type="button" class="btn-sm" id="scan-btn-'+i+'" onclick="scanWifi('+i+')">Scan</button>'+
    '</div>'+
    '<div id="scan-results-'+i+'" class="scan-list" style="display:none"></div>'+
    '<label style="margin-top:.75rem">Password</label>'+
    '<input type="password" name="wifi_password_'+i+'" placeholder="Min. 8 characters">'+
    '<label style="margin-top:.75rem">Security</label>'+
    '<select name="wifi_security_'+i+'">'+securityOptions()+'</select>';
  document.getElementById('extra-nets').appendChild(div);
  netCount++;
  if(netCount>=maxNets)document.getElementById('btn-add-net').style.display='none';
}

function removeNetwork(i){
  var el=document.getElementById('net-block-'+i);
  if(el)el.remove();
  netCount--;
  document.getElementById('btn-add-net').style.display='';
}
</script>
</body></html>`))

type pageData struct {
	Title    string
	Subtitle string
	Error    string
	Body     template.HTML
}

func selectOptions(opts []option, selected string) template.HTML {
	var b bytes.Buffer
	for _, o := range opts {
		sel := ""
		if o.Value == selected {
			sel = " selected"
		}
		fmt.Fprintf(&b, `<option value="%s"%s>%s</option>`, o.Value, sel, o.Label)
	}
	return template.HTML(b.String())
}

func renderPage(w http.ResponseWriter, title, subtitle string, body template.HTML, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTmpl.Execute(w, pageData{Title: title, Subtitle: subtitle, Error: errMsg, Body: body})
}

func renderForm(w http.ResponseWriter, errMsg string) {
	var b bytes.Buffer
	fmt.Fprintf(&b, `<form method="POST" action="/provision">
  <div class="section">
    <div class="stitle">Change Password</div>
    <label>New Password</label>
    <input type="password" name="password" minlength="8" required placeholder="Min. 8 characters">
    <label>Confirm Password</label>
    <input type="password" name="password_confirm" required placeholder="Repeat password">
  </div>
  <div class="section">
    <div class="stitle">Device</div>
    <label>Device Type</label>
    <select name="device_type" required>
      <option value="" disabled selected>Select a type…</option>
      %s
    </select>
    <label>Hostname</label>
    <input type="text" name="hostname" placeholder="Auto-generated from callsign + type">
    <p class="hint">Each device on your network must have a unique hostname.<br>
      mDNS: <strong>&lt;hostname&gt;.local</strong></p>
  </div>
  <div class="section">
    <div class="stitle">Operator</div>
    <label>Callsign</label>
    <input type="text" name="callsign" required placeholder="e.g. W1AW" style="text-transform:uppercase">
    <label>Name <span class="opt">(optional)</span></label>
    <input type="text" name="operator_name" placeholder="e.g. Hiram Percy Maxim">
    <label>Grid Square <span class="opt">(optional)</span></label>
    <input type="text" name="grid_square" placeholder="e.g. FN31" style="text-transform:uppercase">
    <p class="hint">Maidenhead locator — used for distance and bearing calculations.</p>
  </div>
  <div class="section">
    <div class="stitle">Location</div>
    <label>Country</label>
    <select name="country">%s</select>
    <p class="hint">Sets WiFi regulatory domain and operator country for logging.</p>
    <label>Timezone</label>
    <select name="timezone">%s</select>
  </div>
  <div class="section" id="dmrid-section" style="display:none">
    <div class="stitle">DMR ID <span class="opt">(optional)</span></div>
    <label>DMR ID</label>
    <input type="text" name="dmr_id" pattern="[0-9]{7}" maxlength="7" placeholder="e.g. 1234567">
    <p class="hint">Your 7-digit DMR ID from radioid.net. Required for BrandMeister. Can be set later in the web UI.</p>
  </div>
  <div class="section">
    <div class="stitle">WiFi Networks <span class="opt">(optional)</span></div>
    <p class="hint" style="margin-bottom:.75rem">Add one or more networks. The device connects to the highest-priority one in range.
      Leave blank to stay in AP mode.</p>
    <div class="net-block">
      <div class="net-header">
        <span class="net-label">Network 1 — highest priority</span>
      </div>
      <div class="ssid-row">
        <input type="text" id="ssid-input-0" name="ssid" placeholder="Network name (SSID)">
        <button type="button" class="btn-sm" id="scan-btn-0" onclick="scanWifi(0)">Scan</button>
      </div>
      <div id="scan-results-0" class="scan-list" style="display:none"></div>
      <label style="margin-top:.75rem">Password</label>
      <input type="password" name="wifi_password" placeholder="Min. 8 characters">
      <label style="margin-top:.75rem">Security</label>
      <select name="wifi_security">%s</select>
    </div>
    <div id="extra-nets"></div>
    <button type="button" class="btn-add" id="btn-add-net" onclick="addNetwork()">+ Add Another Network</button>
  </div>
  <div class="section">
    <div class="stitle">Management</div>
    <label><input type="checkbox" name="api_enabled" value="true" checked style="width:auto;margin-right:.4rem">
      Enable remote management API (port 7373)</label>
    <p class="hint">If disabled, the device can only be managed via SSH or serial console.</p>
    <label style="margin-top:.75rem"><input type="checkbox" name="mdns_enabled" value="true" checked style="width:auto;margin-right:.4rem">
      Enable mDNS device discovery</label>
    <p class="hint">If disabled, openRig services are hidden from the network. SSH discovery remains active.</p>
  </div>
  <button type="submit">Set Up Device</button>
</form>`,
		selectOptions(deviceTypes, ""),
		selectOptions(countries, "US"),
		selectOptions(timezones, "UTC"),
		selectOptions(securityModes, "auto"),
	)
	renderPage(w, "Setup", "First-time device setup", template.HTML(b.String()), errMsg)
}

func renderDone(w http.ResponseWriter, hostname, deviceType, callsign, operatorName, gridSquare, country, timezone string, dmrID int, networks []wifiNetwork) {
	rows := [][2]string{
		{"Hostname", hostname},
		{"mDNS", hostname + ".local"},
		{"Device Type", strings.ToUpper(deviceType[:1]) + deviceType[1:]},
		{"Callsign", callsign},
	}
	if operatorName != "" {
		rows = append(rows, [2]string{"Name", operatorName})
	}
	if gridSquare != "" {
		rows = append(rows, [2]string{"Grid Square", gridSquare})
	}
	if dmrID > 0 {
		rows = append(rows, [2]string{"DMR ID", fmt.Sprintf("%d", dmrID)})
	}
	rows = append(rows, [2]string{"Country", country})
	rows = append(rows, [2]string{"Timezone", timezone})
	if len(networks) > 0 {
		ssids := make([]string, len(networks))
		for i, n := range networks {
			ssids[i] = n.SSID
		}
		rows = append(rows, [2]string{"WiFi Networks", strings.Join(ssids, ", ")})
	} else {
		rows = append(rows, [2]string{"WiFi", "Not configured — device is in AP mode"})
	}

	var b bytes.Buffer
	b.WriteString(`<div class="done-icon">&#10003;</div>`)
	b.WriteString(`<div style="text-align:center;font-size:1.1rem;font-weight:600;color:#86efac;margin-bottom:1rem">Device provisioned</div>`)
	for _, r := range rows {
		fmt.Fprintf(&b, `<div class="done-row"><strong>%s:</strong> %s</div>`, r[0], r[1])
	}
	fmt.Fprintf(&b,
		`<p class="done-note">You can now SSH to <strong>openrig@%s.local</strong><br>or reconnect to your WiFi network.</p>`,
		hostname)

	renderPage(w, "Setup", "Provisioning complete", template.HTML(b.String()), "")
}

// ── Handlers ──────────────────────────────────────────────────────────────

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	cfg, err := readConfig()
	if err == nil && isProvisioned(cfg) {
		http.Redirect(w, r, "/hotspot", http.StatusFound)
		return
	}
	renderForm(w, "")
}

func handleScan(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ssids, err := scanNetworks()
	if err != nil {
		log.Printf("WiFi scan error: %v", err)
		json.NewEncoder(w).Encode(map[string]any{"networks": []string{}})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"networks": ssids})
}

func handleProvision(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderForm(w, "Could not parse form.")
		return
	}

	password := r.FormValue("password")
	passwordConfirm := r.FormValue("password_confirm")
	deviceType := r.FormValue("device_type")
	hostname := strings.ToLower(r.FormValue("hostname"))
	callsign := strings.ToUpper(strings.TrimSpace(r.FormValue("callsign")))
	operatorName := strings.TrimSpace(r.FormValue("operator_name"))
	gridSquare := strings.ToUpper(strings.TrimSpace(r.FormValue("grid_square")))
	country := r.FormValue("country")
	timezone := r.FormValue("timezone")
	dmrIDStr := strings.TrimSpace(r.FormValue("dmr_id"))
	apiEnabled := r.FormValue("api_enabled") == "true"
	mdnsEnabled := r.FormValue("mdns_enabled") == "true"

	// Collect WiFi networks: primary first, then additional (ssid_1..ssid_4)
	type rawNet struct{ ssid, password, security string }
	var rawNets []rawNet
	if ssid := strings.TrimSpace(r.FormValue("ssid")); ssid != "" {
		rawNets = append(rawNets, rawNet{ssid, r.FormValue("wifi_password"), r.FormValue("wifi_security")})
	}
	for i := 1; i <= 4; i++ {
		ssid := strings.TrimSpace(r.FormValue(fmt.Sprintf("ssid_%d", i)))
		if ssid == "" {
			continue
		}
		rawNets = append(rawNets, rawNet{
			ssid,
			r.FormValue(fmt.Sprintf("wifi_password_%d", i)),
			r.FormValue(fmt.Sprintf("wifi_security_%d", i)),
		})
	}

	// ── Validate ──────────────────────────────────────────────────
	var errs []string
	if len(password) < 8 {
		errs = append(errs, "Password must be at least 8 characters.")
	}
	if password != passwordConfirm {
		errs = append(errs, "Passwords do not match.")
	}
	if deviceType == "" {
		errs = append(errs, "Please select a device type.")
	}
	if callsign == "" {
		errs = append(errs, "Callsign is required.")
	}
	// Validate DMR ID (optional, hotspot only)
	var dmrID int
	if dmrIDStr != "" && deviceType == "hotspot" {
		id, err := strconv.Atoi(dmrIDStr)
		if err != nil || id < 1000000 || id > 9999999 {
			errs = append(errs, "DMR ID must be exactly 7 digits (1000000-9999999).")
		} else {
			dmrID = id
		}
	}

	for _, n := range rawNets {
		if len(n.password) < 8 {
			errs = append(errs, fmt.Sprintf("WiFi password for %q must be at least 8 characters.", n.ssid))
		}
	}
	if len(errs) > 0 {
		renderForm(w, strings.Join(errs, " "))
		return
	}

	// Auto-generate hostname if blank
	if hostname == "" {
		hostname = generateHostname(callsign, deviceType)
	}

	// ── Apply ─────────────────────────────────────────────────────

	if err := changePassword(password); err != nil {
		renderForm(w, fmt.Sprintf("Password change failed: %v", err))
		return
	}

	cfg, err := readConfig()
	if err != nil {
		renderForm(w, fmt.Sprintf("Could not read config: %v", err))
		return
	}

	nested(cfg, "openrig.device.hostname", hostname)
	nested(cfg, "openrig.device.type", deviceType)
	nested(cfg, "openrig.device.timezone", timezone)
	nested(cfg, "openrig.device.provisioned", true)
	nested(cfg, "openrig.operator.callsign", callsign)
	nested(cfg, "openrig.operator.name", operatorName)
	nested(cfg, "openrig.operator.grid_square", gridSquare)
	nested(cfg, "openrig.operator.country", country)
	nested(cfg, "openrig.network.wifi.country", country)
	nested(cfg, "openrig.management.api_enabled", apiEnabled)
	nested(cfg, "openrig.management.mdns_enabled", mdnsEnabled)

	if err := writeConfig(cfg); err != nil {
		renderForm(w, fmt.Sprintf("Could not write config: %v", err))
		return
	}

	applyTimezone(timezone)
	applyHostname(hostname)

	// Write avahi service file based on mdns_enabled setting
	if !devMode {
		if out, err := exec.Command("/usr/local/lib/openrig/update-mdns.sh").CombinedOutput(); err != nil {
			log.Printf("mDNS update error: %v: %s", err, out)
		}
	}

	// Hotspot-specific: write DMR ID and update MMDVM/gateway configs
	if deviceType == "hotspot" {
		if dmrID > 0 {
			nested(cfg, "openrig.hotspot.dmr.dmr_id", dmrID)
			if err := writeConfig(cfg); err != nil {
				log.Printf("DMR ID write error: %v", err)
			}
		}
		if !devMode {
			if out, err := exec.Command("/usr/local/lib/openrig/mmdvm-update.sh").CombinedOutput(); err != nil {
				log.Printf("MMDVM update error: %v: %s", err, out)
			}
		}
	}

	// Build prioritised network list (first = highest priority)
	var networks []wifiNetwork
	for i, n := range rawNets {
		networks = append(networks, wifiNetwork{
			SSID:     n.ssid,
			Password: n.password,
			Security: n.security,
			Priority: len(rawNets) - i, // first gets highest priority
		})
	}

	wifiConfigured := false
	if len(networks) > 0 {
		if err := writeWPAConf(networks, country); err != nil {
			log.Printf("WiFi config error: %v", err)
		} else {
			wifiConfigured = true
		}
	}

	renderDone(w, hostname, deviceType, callsign, operatorName, gridSquare, country, timezone, dmrID, networks)

	// Delay wifi restart so the success page reaches the user first.
	if wifiConfigured && !devMode {
		go func() {
			time.Sleep(3 * time.Second)
			exec.Command("systemctl", "restart", "--no-block", "openrig-wifi").Run()
		}()
	}

	// Start the API service if enabled
	if apiEnabled && !devMode {
		exec.Command("systemctl", "start", "--no-block", "openrig-api").Run()
	}
}

// ── Management page ───────────────────────────────────────────────────────

// configMu serialises config reads and writes from management handlers.
var configMu sync.Mutex

func nestedString(cfg map[string]any, path string) string {
	parts := strings.Split(path, ".")
	m := cfg
	for _, part := range parts[:len(parts)-1] {
		next, ok := m[part].(map[string]any)
		if !ok {
			return ""
		}
		m = next
	}
	v, _ := m[parts[len(parts)-1]].(string)
	return v
}

func nestedBool(cfg map[string]any, path string) bool {
	parts := strings.Split(path, ".")
	m := cfg
	for _, part := range parts[:len(parts)-1] {
		next, ok := m[part].(map[string]any)
		if !ok {
			return false
		}
		m = next
	}
	v, _ := m[parts[len(parts)-1]].(bool)
	return v
}

type liveStatus struct {
	Callsign   string
	Type       string
	Uptime     string
	CPUPercent float64
	MemUsedMB  int
	MemTotalMB int
	APIOnline  bool
}

func fetchLiveStatus() liveStatus {
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest(http.MethodPost, "http://localhost:7373/openrig.v1.DeviceService/GetStatus",
		strings.NewReader("{}"))
	if err != nil {
		return liveStatus{}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return liveStatus{}
	}
	defer resp.Body.Close()
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return liveStatus{}
	}
	uptime := int(toFloat(data["uptime"]))
	return liveStatus{
		Callsign:   toString(data["callsign"]),
		Type:       toString(data["deviceType"]),
		Uptime:     formatUptime(uptime),
		CPUPercent: toFloat(data["cpuPercent"]),
		MemUsedMB:  int(toFloat(data["memUsedMb"])),
		MemTotalMB: int(toFloat(data["memTotalMb"])),
		APIOnline:  true,
	}
}

func formatUptime(s int) string {
	if s < 60 {
		return "< 1m"
	}
	h := s / 3600
	m := (s % 3600) / 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func toFloat(v any) float64 {
	if v == nil {
		return 0
	}
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

type wifiEntry struct {
	SSID     string  `json:"ssid"`
	Security string  `json:"security"`
	Priority float64 `json:"priority"`
}

func fetchWifiNetworks() ([]wifiEntry, string) {
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest(http.MethodPost, "http://localhost:7373/openrig.v1.WifiService/GetWifi",
		strings.NewReader("{}"))
	if err != nil {
		return nil, "WiFi config unavailable"
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return nil, "WiFi config unavailable"
	}
	defer resp.Body.Close()
	var wrapper struct {
		Networks []wifiEntry `json:"networks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return nil, "Failed to parse WiFi config"
	}
	return wrapper.Networks, ""
}

func apiPutWifi(networks []wifiEntry) error {
	payload := struct {
		Config struct {
			Networks []wifiEntry `json:"networks"`
		} `json:"config"`
	}{}
	payload.Config.Networks = networks
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodPost, "http://localhost:7373/openrig.v1.WifiService/UpdateWifi", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("API returned %d", resp.StatusCode)
	}
	return nil
}

func renderManagement(w http.ResponseWriter, apiEnabled, mdnsEnabled bool, hostname, successMsg string, status liveStatus, wifiNets []wifiEntry, wifiErr string) {
	apiChecked := ""
	if apiEnabled {
		apiChecked = " checked"
	}
	mdnsChecked := ""
	if mdnsEnabled {
		mdnsChecked = " checked"
	}
	successHTML := ""
	if successMsg != "" {
		successHTML = `<div style="background:#052e16;border:1px solid #166534;color:#86efac;border-radius:6px;padding:.6rem .75rem;font-size:.875rem;margin-bottom:1rem">` + successMsg + `</div>`
	}

	var b bytes.Buffer

	// Status card
	if status.APIOnline {
		memRow := ""
		if status.MemTotalMB > 0 {
			memRow = fmt.Sprintf(`<tr><td>Memory</td><td>%d / %d MB</td></tr>`, status.MemUsedMB, status.MemTotalMB)
		}
		fmt.Fprintf(&b, `<div style="border:1px solid #334155;border-radius:8px;padding:1rem;margin-bottom:1.25rem">
  <div class="stitle">Device Status</div>
  <table style="width:100%%;font-size:.875rem;color:#cbd5e1">
    <tr><td style="color:#64748b;padding:.25rem 0">Callsign</td><td style="text-align:right;padding:.25rem 0">%s</td></tr>
    <tr><td style="color:#64748b;padding:.25rem 0">Type</td><td style="text-align:right;padding:.25rem 0">%s</td></tr>
    <tr><td style="color:#64748b;padding:.25rem 0">Uptime</td><td style="text-align:right;padding:.25rem 0">%s</td></tr>
    %s
    <tr><td style="color:#64748b;padding:.25rem 0">CPU</td><td style="text-align:right;padding:.25rem 0">%.1f%%</td></tr>
  </table>
</div>`,
			template.HTMLEscapeString(status.Callsign),
			template.HTMLEscapeString(status.Type),
			template.HTMLEscapeString(status.Uptime),
			memRow,
			status.CPUPercent)
	} else {
		b.WriteString(`<p style="color:#64748b;font-size:.875rem;margin-bottom:1.25rem;text-align:center">openrig-api is offline or unreachable.</p>`)
	}

	fmt.Fprintf(&b, `%s<form method="POST" action="/management">
  <div class="section">
    <div class="stitle">Device Management</div>
    <label><input type="checkbox" name="api_enabled" value="true"%s style="width:auto;margin-right:.4rem">
      Enable remote management API (port 7373)</label>
    <p class="hint">If disabled, the device can only be managed via SSH or this page.</p>
    <label style="margin-top:.75rem"><input type="checkbox" name="mdns_enabled" value="true"%s style="width:auto;margin-right:.4rem">
      Enable mDNS device discovery</label>
    <p class="hint">If disabled, openRig services are hidden from the network. SSH discovery remains active.</p>
  </div>
  <p class="hint" style="margin-top:1.25rem;text-align:center">If you disable the API, you can still re-enable it here at <strong>http://%s.local</strong></p>
  <button type="submit">Save Settings</button>
</form>`, successHTML, apiChecked, mdnsChecked, template.HTMLEscapeString(hostname))

	// WiFi section
	b.WriteString(`<div class="section" style="margin-top:1.5rem">
  <div class="stitle">WiFi Networks</div>`)
	if wifiErr != "" {
		fmt.Fprintf(&b, `<p style="color:#64748b;font-size:.875rem">%s</p>`, template.HTMLEscapeString(wifiErr))
	} else {
		if len(wifiNets) == 0 {
			b.WriteString(`<p style="color:#64748b;font-size:.875rem">No networks configured.</p>`)
		} else {
			for _, n := range wifiNets {
				fmt.Fprintf(&b, `<div style="display:flex;align-items:center;justify-content:space-between;padding:.4rem 0;border-bottom:1px solid #1e293b">
  <span style="font-family:monospace;font-size:.875rem;color:#cbd5e1">%s <span style="color:#64748b">(%s, P%d)</span></span>
  <form method="POST" action="/management" style="margin:0">
    <input type="hidden" name="action" value="remove_wifi">
    <input type="hidden" name="ssid" value="%s">
    <button type="submit" style="color:#ef4444;border:1px solid #ef4444;background:none;border-radius:4px;padding:.2rem .5rem;font-size:.75rem;cursor:pointer">Remove</button>
  </form>
</div>`,
					template.HTMLEscapeString(n.SSID),
					template.HTMLEscapeString(n.Security),
					int(n.Priority),
					template.HTMLEscapeString(n.SSID))
			}
		}
		b.WriteString(`<div style="border-top:1px solid #334155;margin-top:.75rem;padding-top:.75rem">
  <form method="POST" action="/management">
    <input type="hidden" name="action" value="add_wifi">
    <label>SSID</label>
    <input type="text" name="wifi_ssid" required placeholder="Network name">
    <label style="margin-top:.5rem">Password</label>
    <input type="password" name="wifi_password" placeholder="Min. 8 characters">
    <label style="margin-top:.5rem">Security</label>
    <select name="wifi_security">
      <option value="auto">WPA2 + WPA3 (recommended)</option>
      <option value="wpa2">WPA2 only</option>
      <option value="wpa3">WPA3 only</option>
    </select>
    <label style="margin-top:.5rem">Priority</label>
    <input type="number" name="wifi_priority" value="10" min="1" max="100" style="width:80px">
    <p class="hint">Higher priority networks are preferred when multiple are in range.</p>
    <button type="submit" style="margin-top:.75rem;background:#0284c7;color:#fff;border:none;border-radius:6px;padding:.5rem 1rem;font-size:.875rem;font-weight:600;cursor:pointer">Add Network</button>
  </form>
</div>`)
	}
	b.WriteString(`</div>`)

	renderPage(w, "Management", "Device management", template.HTML(b.String()), "")
}

func handleManagement(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		handleManagementPost(w, r)
		return
	}

	configMu.Lock()
	cfg, err := readConfig()
	configMu.Unlock()
	if err != nil {
		renderPage(w, "Management", "Device management", "", fmt.Sprintf("Could not read config: %v", err))
		return
	}

	if !isProvisioned(cfg) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	apiEnabled := nestedBool(cfg, "openrig.management.api_enabled")
	mdnsEnabled := nestedBool(cfg, "openrig.management.mdns_enabled")
	hostname := nestedString(cfg, "openrig.device.hostname")

	successMsg := ""
	if r.URL.Query().Get("saved") == "1" {
		successMsg = "Settings saved successfully."
	}

	status := fetchLiveStatus()
	wifiNets, wifiErr := fetchWifiNetworks()
	renderManagement(w, apiEnabled, mdnsEnabled, hostname, successMsg, status, wifiNets, wifiErr)
}

func handleManagementPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderPage(w, "Management", "Device management", "", "Could not parse form.")
		return
	}

	action := r.FormValue("action")

	switch action {
	case "remove_wifi":
		handleWifiRemove(w, r)
		return
	case "add_wifi":
		handleWifiAdd(w, r)
		return
	}

	// Default: save management toggles
	newAPIEnabled := r.FormValue("api_enabled") == "true"
	newMDNSEnabled := r.FormValue("mdns_enabled") == "true"

	configMu.Lock()
	cfg, err := readConfig()
	if err != nil {
		configMu.Unlock()
		renderPage(w, "Management", "Device management", "", fmt.Sprintf("Could not read config: %v", err))
		return
	}

	// Read old values to detect changes
	oldAPIEnabled := nestedBool(cfg, "openrig.management.api_enabled")

	nested(cfg, "openrig.management.api_enabled", newAPIEnabled)
	nested(cfg, "openrig.management.mdns_enabled", newMDNSEnabled)

	if err := writeConfig(cfg); err != nil {
		configMu.Unlock()
		renderPage(w, "Management", "Device management", "", fmt.Sprintf("Could not write config: %v", err))
		return
	}
	configMu.Unlock()

	// Start or stop the API service based on change
	if !devMode {
		if newAPIEnabled && !oldAPIEnabled {
			exec.Command("systemctl", "start", "openrig-api").Run()
		} else if !newAPIEnabled && oldAPIEnabled {
			exec.Command("systemctl", "stop", "openrig-api").Run()
		}

		// Update avahi service file
		if out, err := exec.Command("/usr/local/lib/openrig/update-mdns.sh").CombinedOutput(); err != nil {
			log.Printf("mDNS update error: %v: %s", err, out)
		}
	}

	http.Redirect(w, r, "/management?saved=1", http.StatusSeeOther)
}

func handleWifiRemove(w http.ResponseWriter, r *http.Request) {
	ssid := r.FormValue("ssid")
	if ssid == "" {
		http.Redirect(w, r, "/management", http.StatusSeeOther)
		return
	}
	networks, errMsg := fetchWifiNetworks()
	if errMsg != "" {
		log.Printf("WiFi remove: %s", errMsg)
		http.Redirect(w, r, "/management", http.StatusSeeOther)
		return
	}
	var filtered []wifiEntry
	for _, n := range networks {
		if n.SSID != ssid {
			filtered = append(filtered, n)
		}
	}
	if err := apiPutWifi(filtered); err != nil {
		log.Printf("WiFi remove PUT error: %v", err)
	}
	http.Redirect(w, r, "/management?saved=1", http.StatusSeeOther)
}

func handleWifiAdd(w http.ResponseWriter, r *http.Request) {
	ssid := strings.TrimSpace(r.FormValue("wifi_ssid"))
	password := r.FormValue("wifi_password")
	security := r.FormValue("wifi_security")
	priorityStr := r.FormValue("wifi_priority")

	if ssid == "" {
		http.Redirect(w, r, "/management", http.StatusSeeOther)
		return
	}
	priority := 10.0
	if v, err := strconv.ParseFloat(priorityStr, 64); err == nil {
		priority = v
	}
	if security == "" {
		security = "auto"
	}

	networks, _ := fetchWifiNetworks()
	// Use a struct with password for the PUT payload
	type wifiPutEntry struct {
		SSID     string  `json:"ssid"`
		Password string  `json:"password,omitempty"`
		Security string  `json:"security"`
		Priority float64 `json:"priority"`
	}
	var putNets []wifiPutEntry
	for _, n := range networks {
		putNets = append(putNets, wifiPutEntry{
			SSID:     n.SSID,
			Security: n.Security,
			Priority: n.Priority,
		})
	}
	putNets = append(putNets, wifiPutEntry{
		SSID:     ssid,
		Password: password,
		Security: security,
		Priority: priority,
	})

	data, err := json.Marshal(map[string]any{"config": map[string]any{"networks": putNets}})
	if err != nil {
		log.Printf("WiFi add marshal error: %v", err)
		http.Redirect(w, r, "/management", http.StatusSeeOther)
		return
	}
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodPost, "http://localhost:7373/openrig.v1.WifiService/UpdateWifi", bytes.NewReader(data))
	if err != nil {
		log.Printf("WiFi add request error: %v", err)
		http.Redirect(w, r, "/management", http.StatusSeeOther)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("WiFi add PUT error: %v", err)
		http.Redirect(w, r, "/management", http.StatusSeeOther)
		return
	}
	resp.Body.Close()

	http.Redirect(w, r, "/management?saved=1", http.StatusSeeOther)
}

// ── Hotspot management UI (proxies /api/* to openrig-api on :7373) ────────

var apiTarget, _ = url.Parse("http://localhost:7373")
var apiProxy = httputil.NewSingleHostReverseProxy(apiTarget)

func handleAPIProxy(w http.ResponseWriter, r *http.Request) {
	apiProxy.ServeHTTP(w, r)
}

// handleGatewayCmd publishes a remote-control command to a gateway via MQTT.
// POST /gateway-cmd  {"gateway":"ysf"|"dmr", "command":"LinkYSF US-KCWIDE"|"UnLink"|"enable net1"|"disable net1"}
func handleGatewayCmd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Gateway string `json:"gateway"`
		Command string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var topic string
	switch req.Gateway {
	case "ysf":
		topic = "ysf-gateway/command"
	case "dmr":
		topic = "dmr-gateway/command"
	default:
		http.Error(w, "invalid gateway", http.StatusBadRequest)
		return
	}
	// Basic sanity check — no newlines, reasonable length
	cmd := req.Command
	if cmd == "" || len(cmd) > 200 || strings.ContainsAny(cmd, "\n\r") {
		http.Error(w, "invalid command", http.StatusBadRequest)
		return
	}
	if err := exec.Command("mosquitto_pub", "-h", "localhost", "-t", topic, "-m", cmd).Run(); err != nil {
		http.Error(w, "publish failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

// uiTmpl is the full management SPA, served at /hotspot.
// JavaScript uses relative /api/* paths which are proxied to openrig-api.
var uiTmpl = template.Must(template.New("ui").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>openRig Management</title>
<link rel="icon" type="image/svg+xml" href="/favicon.svg">
<style>
  *,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
  body{font-family:system-ui,-apple-system,sans-serif;background:#0f172a;color:#e2e8f0;min-height:100vh}
  .header{background:#1e293b;border-bottom:1px solid #334155;padding:1rem 1.5rem;display:flex;align-items:center;justify-content:space-between}
  .logo{font-size:1.2rem;font-weight:700;color:#38bdf8}
  .status-badge{font-size:.75rem;padding:.25rem .6rem;border-radius:999px;background:#065f46;color:#6ee7b7}
  #conn-warn{display:none;background:#7f1d1d;color:#fca5a5;text-align:center;padding:.5rem 1rem;font-size:.85rem;border-bottom:1px solid #ef4444}
  .tabs{display:flex;background:#1e293b;border-bottom:1px solid #334155;padding:0 1rem;gap:0}
  .tab{padding:.75rem 1.25rem;font-size:.875rem;color:#64748b;cursor:pointer;border-bottom:2px solid transparent;
    transition:color .15s,border-color .15s;background:none;border-top:none;border-left:none;border-right:none}
  .tab:hover{color:#94a3b8}
  .tab.active{color:#38bdf8;border-bottom-color:#38bdf8}
  .content{max-width:720px;margin:0 auto;padding:1.5rem}
  .panel{display:none}
  .panel.active{display:block}
  .card{background:#1e293b;border:1px solid #334155;border-radius:12px;padding:1.5rem;margin-bottom:1rem}
  .card-title{font-size:.7rem;font-weight:700;text-transform:uppercase;letter-spacing:.08em;color:#64748b;margin-bottom:1rem}
  .stat-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(140px,1fr));gap:.75rem}
  .stat{background:#0f172a;border-radius:8px;padding:.75rem}
  .stat-label{font-size:.7rem;color:#64748b;text-transform:uppercase;letter-spacing:.05em}
  .stat-value{font-size:1.1rem;font-weight:600;color:#e2e8f0;margin-top:.15rem}
  label{display:block;font-size:.85rem;color:#94a3b8;margin-bottom:.25rem;margin-top:.85rem}
  label:first-child{margin-top:0}
  input,select{width:100%;background:#0f172a;border:1px solid #475569;border-radius:6px;
    padding:.6rem .75rem;color:#e2e8f0;font-size:.95rem;outline:none;transition:border-color .15s}
  input:focus,select:focus{border-color:#38bdf8}
  .hint{font-size:.75rem;color:#64748b;margin-top:.3rem}
  .combo{position:relative}
  .combo-input{width:100%;background:#0f172a;border:1px solid #475569;border-radius:6px;padding:.6rem .75rem;color:#e2e8f0;font-size:.95rem;outline:none;transition:border-color .15s}
  .combo-input:focus{border-color:#38bdf8}
  .combo-dropdown{position:absolute;z-index:50;width:100%;max-height:180px;overflow-y:auto;background:#1e293b;border:1px solid #475569;border-radius:6px;margin-top:2px;display:none;box-shadow:0 4px 12px rgba(0,0,0,.4)}
  .combo-dropdown.open{display:block}
  .combo-option{padding:.45rem .75rem;font-size:.9rem;cursor:pointer;color:#e2e8f0}
  .combo-option:hover,.combo-option.active{background:#0f172a;color:#38bdf8}
  .toggle-row{display:flex;justify-content:space-between;align-items:center;padding:.65rem 0;border-bottom:1px solid #1e293b}
  .toggle-row:last-child{border-bottom:none}
  .toggle-label{font-size:.9rem;color:#e2e8f0}
  .toggle-sub{font-size:.75rem;color:#64748b}
  .switch{position:relative;width:44px;height:24px;flex-shrink:0}
  .switch input{opacity:0;width:0;height:0}
  .slider{position:absolute;inset:0;background:#475569;border-radius:999px;cursor:pointer;transition:background .2s}
  .slider::before{content:'';position:absolute;left:3px;top:3px;width:18px;height:18px;
    background:#e2e8f0;border-radius:50%;transition:transform .2s}
  .switch input:checked+.slider{background:#0284c7}
  .switch input:checked+.slider::before{transform:translateX(20px)}
  .btn{background:#0284c7;color:#fff;border:none;border-radius:6px;padding:.65rem 1.25rem;
    font-size:.9rem;font-weight:600;cursor:pointer;transition:background .15s}
  .btn:hover{background:#0369a1}
  .btn:disabled{opacity:.5;cursor:not-allowed}
  .btn-outline{background:transparent;color:#38bdf8;border:1px solid #1e40af;padding:.5rem 1rem;font-size:.85rem}
  .btn-outline:hover{background:#1e3a5f}
  .btn-danger{background:#450a0a;color:#fca5a5;border:1px solid #7f1d1d}
  .btn-danger:hover{background:#7f1d1d}
  .btn-row{display:flex;gap:.5rem;margin-top:1rem;justify-content:flex-end}
  .tg-table{width:100%;border-collapse:collapse;margin-top:.75rem}
  .tg-table th{text-align:left;font-size:.7rem;text-transform:uppercase;letter-spacing:.05em;
    color:#64748b;padding:.4rem .5rem;border-bottom:1px solid #334155}
  .tg-table td{padding:.4rem .5rem;font-size:.875rem;border-bottom:1px solid #1e293b}
  .tg-table input{padding:.4rem .5rem;font-size:.85rem}
  .tg-actions{width:60px;text-align:right}
  .clients-table{width:100%;border-collapse:collapse;margin-top:.5rem}
  .clients-table th{text-align:left;font-size:.7rem;text-transform:uppercase;letter-spacing:.05em;
    color:#64748b;padding:.5rem;border-bottom:1px solid #334155}
  .clients-table td{padding:.5rem;font-size:.875rem;border-bottom:1px solid #1e293b}
  .empty{text-align:center;color:#475569;padding:2rem;font-size:.9rem}
  .lm-row{display:flex;align-items:center;gap:.75rem}
  .lm-row .combo{flex:1}
  .lm-label{font-size:.85rem;color:#94a3b8;white-space:nowrap}
  .btn-sm{padding:.45rem .9rem;font-size:.82rem}
  .btn-unlink{background:transparent;color:#f87171;border:1px solid #7f1d1d;border-radius:6px;padding:.45rem .9rem;font-size:.82rem;cursor:pointer;transition:background .15s}
  .btn-unlink:hover{background:#7f1d1d}
  .net-block{border:1px solid #334155;border-radius:8px;padding:1rem;margin-top:.75rem}
  .net-header{display:flex;justify-content:space-between;align-items:center;margin-bottom:.5rem}
  .net-label{font-size:.8rem;font-weight:600;color:#94a3b8}
  .btn-add{width:100%;margin-top:.75rem;background:transparent;color:#38bdf8;border:1px dashed #334155;
    border-radius:6px;padding:.6rem;font-size:.875rem;cursor:pointer}
  .btn-add:hover{border-color:#38bdf8}
  .toast{position:fixed;bottom:1.5rem;right:1.5rem;background:#065f46;color:#6ee7b7;
    padding:.75rem 1.25rem;border-radius:8px;font-size:.875rem;font-weight:500;
    transform:translateY(100px);opacity:0;transition:all .3s;z-index:100}
  .toast.show{transform:translateY(0);opacity:1}
  .toast.error{background:#450a0a;color:#fca5a5}
  .svc-row{display:flex;justify-content:space-between;align-items:center;padding:.5rem 0;border-bottom:1px solid #1e293b}
  .svc-row:last-child{border-bottom:none}
  .svc-name{font-size:.9rem;color:#e2e8f0}
  #heard-map{height:320px;border-radius:8px;margin-top:1rem;background:#1e293b}
  .hs-hdr{background:#7c2d12;color:#fcd34d;font-size:.68rem;font-weight:700;letter-spacing:.07em;text-transform:uppercase;text-align:center;padding:.28rem .5rem;border-radius:4px;margin-bottom:2px}
  .hs-modes{display:grid;grid-template-columns:1fr 1fr;gap:2px;margin-bottom:3px}
  .hs-mode{background:#1e293b;color:#475569;text-align:center;padding:.3rem .4rem;font-size:.78rem;font-weight:600;border-radius:3px}
  .hs-mode.on{background:#15803d;color:#bbf7d0}
  .hs-kv{display:flex;border-bottom:1px solid #0f172a;min-height:1.75rem}
  .hs-kv:last-child{border-bottom:none}
  .hs-k{background:#1e293b;color:#94a3b8;font-size:.7rem;font-weight:700;padding:.3rem .4rem;min-width:2.8rem;text-align:center;display:flex;align-items:center;justify-content:center;flex-shrink:0}
  .hs-v{color:#e2e8f0;font-size:.78rem;padding:.3rem .5rem;flex:1;display:flex;align-items:center}
  .hs-full{background:#1e293b;border-radius:3px;color:#e2e8f0;font-size:.78rem;text-align:center;padding:.3rem .5rem;margin-top:2px}
  .hs-full.linked{background:#14532d;color:#86efac}
  .hs-full.linking{background:#713f12;color:#fde68a}
  .hs-full.unlinked{background:#450a0a;color:#fca5a5}
  .leaflet-popup-content-wrapper{background:#1e293b;color:#e2e8f0;border:1px solid #334155}
  .leaflet-popup-tip{background:#1e293b}
  .map-call-link{color:#38bdf8;font-weight:700;cursor:pointer;text-decoration:none}
  .map-call-link:hover{text-decoration:underline}
</style>
<link rel="stylesheet" href="https://unpkg.com/leaflet@1.9.4/dist/leaflet.css">
<script src="https://unpkg.com/leaflet@1.9.4/dist/leaflet.js"></script>
</head>
<body>
<div id="conn-warn">&#9888; Connection lost — retrying... (<span id="conn-warn-count">0</span> failed attempts)</div>
<div class="header">
  <div style="display:flex;align-items:center;gap:.6rem"><svg xmlns="http://www.w3.org/2000/svg" viewBox="-10 -10 220 220" width="28" height="28" fill="none" style="flex-shrink:0"><rect stroke="#38bdf8" stroke-width="5" stroke-linecap="round" stroke-linejoin="round" x="12" y="52" width="176" height="120" rx="9"/><path stroke="#38bdf8" stroke-width="5" fill="none" stroke-linecap="round" stroke-linejoin="round" d="M62 52Q62 32 100 32Q138 32 138 52"/><circle stroke="#38bdf8" stroke-width="5" fill="none" cx="72" cy="114" r="46"/><circle stroke="#38bdf8" stroke-width="5" fill="none" cx="72" cy="114" r="19"/><circle stroke="#38bdf8" stroke-width="5" fill="#38bdf8" cx="72" cy="114" r="7"/><rect stroke="#38bdf8" stroke-width="5" stroke-linecap="round" stroke-linejoin="round" x="128" y="64" width="48" height="30" rx="4"/><circle stroke="#38bdf8" stroke-width="5" fill="none" cx="170" cy="148" r="16"/></svg><div class="logo">openRig</div></div>
  <div class="status-badge" id="status-badge">Loading...</div>
</div>
<div class="tabs">
  <button class="tab active" onclick="showTab('status',event)">Status</button>
  <button class="tab" onclick="showTab('hotspot',event)">Hotspot</button>
  <button class="tab" onclick="showTab('network',event)">Network</button>
  <button class="tab" onclick="showTab('device',event)">Device</button>
</div>
<div class="content">

<!-- Status Panel -->
<div class="panel active" id="panel-status" style="width:100vw;position:relative;left:50%;transform:translateX(-50%);padding:1.5rem">
  <div style="display:grid;grid-template-columns:260px 1fr 520px;gap:1.25rem;align-items:start">
    <!-- Left: Device Info + Hotspot Status -->
    <div style="position:sticky;top:1.5rem">
    <div class="card" style="margin-bottom:1.25rem">
      <div class="card-title">Device Info</div>
      <div class="stat-grid" id="status-grid">
        <div class="stat"><div class="stat-label">Callsign</div><div class="stat-value" id="st-callsign">--</div></div>
        <div class="stat"><div class="stat-label">Hostname</div><div class="stat-value" id="st-hostname">--</div></div>
        <div class="stat"><div class="stat-label">Device Type</div><div class="stat-value" id="st-type">--</div></div>
        <div class="stat"><div class="stat-label">Version</div><div class="stat-value" id="st-version">--</div></div>
        <div class="stat"><div class="stat-label">Uptime</div><div class="stat-value" id="st-uptime">--</div></div>
        <div class="stat"><div class="stat-label">Provisioned</div><div class="stat-value" id="st-provisioned">--</div></div>
      </div>
    </div>
      <div class="card" id="hs-card" style="margin-bottom:0;display:none">
        <div class="card-title">Hotspot Status</div>
        <div class="hs-hdr">Modes Enabled</div>
        <div class="hs-modes">
          <div class="hs-mode" id="hs-dmr">DMR</div>
          <div class="hs-mode" id="hs-ysf">YSF</div>
          <div class="hs-mode" id="hs-ysf2dmr">YSF&#8594;DMR</div>
          <div class="hs-mode" id="hs-dmr2ysf">DMR&#8594;YSF</div>
        </div>
        <div class="hs-hdr">Radio Info</div>
        <div class="hs-kv"><div class="hs-k">Freq</div><div class="hs-v" id="hs-freq">--</div></div>
        <div class="hs-kv" id="hs-cc-row"><div class="hs-k">CC</div><div class="hs-v" id="hs-cc">--</div></div>
        <div class="hs-kv" id="hs-id-row"><div class="hs-k">ID</div><div class="hs-v" id="hs-id">--</div></div>
        <div class="hs-hdr" style="margin-top:.25rem" id="hs-net-hdr">Network</div>
        <div class="hs-full" id="hs-net">--</div>
      </div>
    </div>
    <!-- Center: Link Manager + Last Heard -->
    <div>
      <!-- Link Manager — visible only when YSF/DMR enabled -->
      <div id="lm-container" style="display:none">
        <div id="lm-ysf-panel" style="display:none">
          <div class="card" style="margin-bottom:.75rem;padding:1rem 1.25rem">
            <div class="card-title" style="margin-bottom:.75rem">YSF Reflector Manager</div>
            <div class="lm-row">
              <span class="lm-label">Reflector</span>
              <div class="combo"><input type="text" class="combo-input" id="lm-ysf-ref-input" placeholder="Search reflectors..." autocomplete="off" spellcheck="false"><div class="combo-dropdown" id="lm-ysf-ref-dropdown"></div><input type="hidden" id="lm-ysf-ref"></div>
              <button class="btn btn-sm" onclick="lmRequestChange('ysf','link')">Link</button>
              <button class="btn-unlink" onclick="lmRequestChange('ysf','unlink')">Unlink</button>
            </div>
          </div>
        </div>
      </div>
      <div class="card" style="margin-bottom:0">
        <div class="card-title">Last Heard</div>
        <table class="clients-table" id="lastHeardTable" style="display:none">
          <thead><tr><th>Callsign</th><th>Mode</th><th>Info</th><th>Duration</th><th>BER</th><th>Time</th></tr></thead>
          <tbody id="lastHeardBody"></tbody>
        </table>
        <p id="lastHeardEmpty" class="empty" style="text-align:center;padding:.75rem 0">No recent activity</p>
      </div>
    </div>
    <!-- Right: Map -->
    <div id="status-right-col">
      <div class="card" style="margin-bottom:0">
        <div class="card-title">Last Heard — Map</div>
        <div id="heard-map"></div>
        <div id="heard-detail" style="display:none;margin-top:.75rem;padding:.75rem;background:#0f172a;border-radius:6px;display:flex;gap:1rem;align-items:flex-start">
          <img id="hd-img" src="" alt="" style="width:72px;height:72px;border-radius:6px;object-fit:cover;display:none">
          <div>
            <div style="font-size:1.1rem;font-weight:700"><a id="hd-call" href="#" target="_blank" style="color:#38bdf8;text-decoration:none"></a> <span id="hd-name" style="color:#94a3b8;font-weight:400;font-size:.95rem"></span></div>
            <div id="hd-loc" style="color:#64748b;font-size:.85rem;margin-top:.2rem"></div>
            <div id="hd-grid" style="color:#64748b;font-size:.85rem"></div>
            <div id="hd-class" style="color:#64748b;font-size:.85rem"></div>
          </div>
        </div>
      </div>
    </div>
  </div>
</div>

<!-- Hotspot Panel -->
<div class="panel" id="panel-hotspot">
  <div class="card">
    <div class="card-title">Modem</div>
    <label>Modem Type</label>
    <select id="modem-type" onchange="onModemTypeChange()">
      <option value="mmdvm_hs_hat">MMDVM_HS_Hat (RPi GPIO)</option>
      <option value="mmdvm_hs_dual_hat">MMDVM_HS_Dual_Hat (RPi GPIO, dual-band)</option>
      <option value="zumspot">ZUMspot (USB)</option>
      <option value="dvmega">DVMega (USB)</option>
      <option value="nano_hotspot">Nano hotSPOT (USB)</option>
      <option value="custom">Custom / Other</option>
    </select>
    <label>Serial Port</label>
    <input type="text" id="modem-port" placeholder="e.g. /dev/ttyAMA0">
    <p class="hint">Auto-filled from modem type. Override if your setup differs.</p>
    <label>RX Frequency (MHz)</label>
    <input type="number" id="rf-frequency" step="0.0001" min="100" max="6000" placeholder="e.g. 438.8000">
    <label>TX Frequency (MHz) <span style="color:#64748b;font-weight:400">— leave 0 for simplex (same as RX)</span></label>
    <input type="number" id="tx-frequency" step="0.0001" min="0" max="6000" placeholder="0 = same as RX">
  </div>
  <div class="card">
    <div class="card-title">DMR Configuration</div>
    <div class="toggle-row">
      <div><div class="toggle-label">DMR Enabled</div><div class="toggle-sub">Digital Mobile Radio gateway</div></div>
      <label class="switch"><input type="checkbox" id="dmr-enabled"><span class="slider"></span></label>
    </div>
    <label>DMR ID</label>
    <div style="display:flex;gap:.5rem;align-items:center">
      <input type="text" id="dmr-id" inputmode="numeric" pattern="[0-9]{7}" maxlength="7" placeholder="7-digit ID" style="flex:1" oninput="updateEffectiveDmrId()">
      <span style="color:#64748b;white-space:nowrap;font-size:.9rem">Suffix</span>
      <input type="number" id="dmr-id-suffix" min="0" max="99" value="0" style="width:4rem" oninput="updateEffectiveDmrId()">
    </div>
    <p class="hint" id="dmr-id-effective" style="margin-top:.25rem"></p>
    <label>Color Code (1-15)</label>
    <input type="number" id="dmr-colorcode" min="1" max="15" value="1">
    <label>Network</label>
    <select id="dmr-network" onchange="onDmrNetworkChange()">
      <option value="brandmeister">BrandMeister</option>
      <option value="dmrplus">DMR+</option>
      <option value="freedmr">FreeDMR</option>
      <option value="tgif">TGIF</option>
      <option value="systemx">SystemX</option>
      <option value="xlx">XLX</option>
      <option value="custom">Custom</option>
    </select>
    <label>Server</label>
    <div class="combo"><input type="text" class="combo-input" id="dmr-server-input" placeholder="Loading..." autocomplete="off" spellcheck="false"><div class="combo-dropdown" id="dmr-server-dropdown"></div><input type="hidden" id="dmr-server"></div>
    <label>Password</label>
    <input type="password" id="dmr-password" placeholder="DMR network password">
    <div class="card-title" style="margin-top:1.25rem">Talkgroups</div>
    <table class="tg-table">
      <thead><tr><th>TG</th><th>Slot</th><th>Name</th><th class="tg-actions"></th></tr></thead>
      <tbody id="tg-body"></tbody>
    </table>
    <button type="button" class="btn-add" onclick="addTalkgroup()">+ Add Talkgroup</button>
  </div>
  <div class="card">
    <div class="card-title">YSF Configuration</div>
    <div class="toggle-row">
      <div><div class="toggle-label">YSF Enabled</div><div class="toggle-sub">Yaesu System Fusion reflector</div></div>
      <label class="switch"><input type="checkbox" id="ysf-enabled"><span class="slider"></span></label>
    </div>
    <label>Network Type</label>
    <select id="ysf-network" onchange="onYsfNetworkChange()">
      <option value="ysf">YSF Reflector</option>
      <option value="fcs">FCS Room</option>
      <option value="custom">Custom</option>
    </select>
    <div id="ysf-reflector-group">
      <label>Reflector</label>
      <div class="combo"><input type="text" class="combo-input" id="ysf-reflector-input" placeholder="Loading..." autocomplete="off" spellcheck="false"><div class="combo-dropdown" id="ysf-reflector-dropdown"></div><input type="hidden" id="ysf-reflector"></div>
    </div>
    <div id="ysf-fcs-group" style="display:none">
      <label>FCS Room</label>
      <div class="combo"><input type="text" class="combo-input" id="fcs-room-input" placeholder="Loading..." autocomplete="off" spellcheck="false"><div class="combo-dropdown" id="fcs-room-dropdown"></div><input type="hidden" id="fcs-room"></div>
      <label>Module</label>
      <select id="fcs-module">
        <option value="A">A</option><option value="B">B</option><option value="C">C</option>
        <option value="D">D</option><option value="E">E</option><option value="F">F</option>
        <option value="G">G</option><option value="H">H</option><option value="I">I</option>
        <option value="J">J</option><option value="K">K</option><option value="L">L</option>
        <option value="M">M</option><option value="N">N</option><option value="O">O</option>
        <option value="P">P</option><option value="Q">Q</option><option value="R">R</option>
        <option value="S">S</option><option value="T">T</option><option value="U">U</option>
        <option value="V">V</option><option value="W">W</option><option value="X">X</option>
        <option value="Y">Y</option><option value="Z">Z</option>
      </select>
    </div>
    <div id="ysf-custom-group" style="display:none">
      <label>Server</label>
      <input type="text" id="ysf-custom-server" placeholder="Custom YSF server address">
    </div>
    <label>Description</label>
    <input type="text" id="ysf-description" placeholder="Station description">
    <div class="toggle-row" style="margin-top:.75rem">
      <div><div class="toggle-label">WiresX Passthrough</div><div class="toggle-sub">Forward WiresX room commands to the reflector instead of handling locally</div></div>
      <label class="switch"><input type="checkbox" id="ysf-wiresx-passthrough"><span class="slider"></span></label>
    </div>
  </div>
  <div class="card">
    <div class="card-title">Cross-Mode Bridge</div>
    <div class="toggle-row">
      <div><div class="toggle-label">YSF to DMR</div><div class="toggle-sub">Bridge YSF audio to a DMR talkgroup</div></div>
      <label class="switch"><input type="checkbox" id="ysf2dmr-enabled"><span class="slider"></span></label>
    </div>
    <label>DMR Talkgroup</label>
    <div style="display:flex;align-items:center;gap:.5rem">
      <input type="number" id="ysf2dmr-tg" placeholder="e.g. 31672" style="flex:1">
      <span id="ysf2dmrTGName" style="color:#6c757d;font-size:.85rem;white-space:nowrap"></span>
    </div>
  </div>
  <div class="btn-row">
    <button class="btn" onclick="saveHotspot()">Save Hotspot Config</button>
  </div>
</div>

<!-- Network Panel -->
<div class="panel" id="panel-network">
  <div class="card">
    <div class="card-title">WiFi Networks</div>
    <p class="hint" style="margin-bottom:.75rem">Networks are tried in priority order. First network has highest priority.</p>
    <div id="wifi-nets"></div>
    <button type="button" class="btn-add" id="btn-add-wifi" onclick="addWifiNetwork()">+ Add Network</button>
  </div>
  <div class="btn-row">
    <button class="btn" onclick="saveWifi()">Save WiFi Config</button>
  </div>
  <div class="card" style="margin-top:1rem">
    <div class="card-title">Services</div>
    <div class="svc-row">
      <span class="svc-name">WiFi Manager</span>
      <button class="btn btn-outline" onclick="restartSvc('wifi')">Restart</button>
    </div>
    <div class="svc-row">
      <span class="svc-name">DMR Gateway</span>
      <button class="btn btn-outline" onclick="restartSvc('dmr')">Restart</button>
    </div>
    <div class="svc-row">
      <span class="svc-name">YSF Gateway</span>
      <button class="btn btn-outline" onclick="restartSvc('ysf')">Restart</button>
    </div>
    <div class="svc-row">
      <span class="svc-name">YSF2DMR Bridge</span>
      <button class="btn btn-outline" onclick="restartSvc('ysf2dmr')">Restart</button>
    </div>
  </div>
</div>

<!-- Device Panel -->
<div class="panel" id="panel-device">
  <div class="card">
    <div class="card-title">Device Configuration</div>
    <label>Callsign</label>
    <input type="text" id="dev-callsign" style="text-transform:uppercase">
    <label>Hostname</label>
    <input type="text" id="dev-hostname">
    <p class="hint">mDNS: &lt;hostname&gt;.local</p>
    <label>Operator Name</label>
    <input type="text" id="dev-name">
    <label>Grid Square</label>
    <input type="text" id="dev-grid" style="text-transform:uppercase">
    <p class="hint">Maidenhead locator for distance and bearing calculations.</p>
    <label>QRZ Username</label>
    <input type="text" id="dev-qrz-user" autocomplete="username">
    <label>QRZ Password</label>
    <input type="password" id="dev-qrz-pass" autocomplete="current-password">
    <p class="hint">Used for callsign lookups on the Status map. Requires a QRZ XML subscription.</p>
    <button class="btn" style="margin-top:.25rem" onclick="testQrzCreds()">Test QRZ Credentials</button>
    <p id="qrz-test-result" style="margin-top:.5rem;font-size:.85rem;display:none"></p>
    <label>Timezone</label>
    <select id="dev-timezone">
      <option value="UTC">UTC</option>
      <option value="America/New_York">US Eastern</option>
      <option value="America/Chicago">US Central</option>
      <option value="America/Denver">US Mountain</option>
      <option value="America/Los_Angeles">US Pacific</option>
      <option value="America/Anchorage">US Alaska</option>
      <option value="Pacific/Honolulu">US Hawaii</option>
      <option value="America/Toronto">Canada Eastern</option>
      <option value="America/Vancouver">Canada Pacific</option>
      <option value="America/Sao_Paulo">Brazil</option>
      <option value="America/Mexico_City">Mexico</option>
      <option value="America/Argentina/Buenos_Aires">Argentina</option>
      <option value="Europe/London">UK</option>
      <option value="Europe/Dublin">Ireland</option>
      <option value="Europe/Paris">France / Central Europe</option>
      <option value="Europe/Berlin">Germany</option>
      <option value="Europe/Amsterdam">Netherlands</option>
      <option value="Europe/Stockholm">Sweden</option>
      <option value="Europe/Oslo">Norway</option>
      <option value="Europe/Helsinki">Finland</option>
      <option value="Europe/Rome">Italy</option>
      <option value="Europe/Madrid">Spain</option>
      <option value="Asia/Tokyo">Japan</option>
      <option value="Asia/Shanghai">China</option>
      <option value="Asia/Kolkata">India</option>
      <option value="Asia/Dubai">UAE</option>
      <option value="Australia/Sydney">Australia Eastern</option>
      <option value="Australia/Perth">Australia Western</option>
      <option value="Pacific/Auckland">New Zealand</option>
      <option value="Africa/Johannesburg">South Africa</option>
      <option value="Africa/Cairo">Egypt</option>
    </select>
  </div>
  <div class="btn-row">
    <button class="btn" onclick="saveDevice()">Save Device Config</button>
  </div>
</div>

</div>

<div class="toast" id="toast"></div>

<script src="/wasm_exec.js"></script>
<script>
function showTab(name,e){
  document.querySelectorAll('.tab').forEach(function(t){t.classList.remove('active');});
  document.querySelectorAll('.panel').forEach(function(p){p.classList.remove('active');});
  document.getElementById('panel-'+name).classList.add('active');
  if(e&&e.target)e.target.classList.add('active');
}
function toast(msg,isError){
  var el=document.getElementById('toast');
  el.textContent=msg;
  el.className='toast'+(isError?' error':'')+' show';
  setTimeout(function(){el.className='toast';},3000);
}
function formatUptime(s){
  if(!s||s<60)return'< 1m';
  var h=Math.floor(s/3600);var m=Math.floor((s%3600)/60);
  if(h>0)return h+'h '+m+'m';
  return m+'m';
}
function renderStatus(d){
  document.getElementById('st-callsign').textContent=d.callsign||'--';
  document.getElementById('st-hostname').textContent=d.hostname||'--';
  document.getElementById('st-type').textContent=d.deviceType||'--';
  document.getElementById('st-version').textContent=d.version||'--';
  document.getElementById('st-uptime').textContent=formatUptime(d.uptime);
  document.getElementById('st-provisioned').textContent=d.provisioned?'Yes':'No';
  document.getElementById('status-badge').textContent=d.callsign?d.callsign+' \u00b7 '+d.deviceType:'Not provisioned';
}
var combos={};
function makeCombo(id){
  var allItems=[];  // stored values (e.g. "AMERICA")
  var allLabels=[]; // display text (e.g. "AMERICA (00029)"); parallel to allItems
  function labelFor(value){
    var i=allItems.indexOf(value);
    return(i>=0&&allLabels[i])?allLabels[i]:value;
  }
  function el(){return{inp:document.getElementById(id+'-input'),drop:document.getElementById(id+'-dropdown'),hidden:document.getElementById(id)};}
  function renderList(q){
    var e=el();if(!e.drop)return;
    var lq=q?q.toLowerCase():'';
    var filtered=allItems.reduce(function(acc,v,i){
      var lbl=allLabels[i]||v;
      if(!lq||lbl.toLowerCase().indexOf(lq)>=0)acc.push({value:v,label:lbl});
      return acc;
    },[]);
    e.drop.innerHTML=filtered.map(function(item){
      return'<div class="combo-option" data-value="'+esc(item.value)+'">'+esc(item.label)+'</div>';
    }).join('');
    e.drop.classList.toggle('open',filtered.length>0);
  }
  function select(value,close){
    var e=el();
    if(e.hidden)e.hidden.value=value;
    if(e.inp)e.inp.value=labelFor(value);
    if(close&&e.drop)e.drop.classList.remove('open');
  }
  var inpEl=document.getElementById(id+'-input');
  var dropEl=document.getElementById(id+'-dropdown');
  if(inpEl){
    inpEl.addEventListener('focus',function(){inpEl.value='';renderList('');});
    inpEl.addEventListener('input',function(){renderList(inpEl.value);});
    inpEl.addEventListener('blur',function(){
      setTimeout(function(){
        var d=document.getElementById(id+'-dropdown');
        if(d)d.classList.remove('open');
        // Restore the label for the current stored value on blur.
        var h=document.getElementById(id);
        var i=document.getElementById(id+'-input');
        if(h&&i)i.value=labelFor(h.value);
      },150);
    });
  }
  if(dropEl){
    dropEl.addEventListener('mousedown',function(e){
      e.preventDefault();
      var opt=e.target.closest('.combo-option');
      if(opt)select(opt.dataset.value,true);
    });
  }
  combos[id]={populate:function(items,selected,labels){
    allItems=items;
    allLabels=labels||[];
    var effective=(selected&&items.indexOf(selected)>=0)?selected:(items.length?items[0]:'');
    select(effective,false);
  }};
}
function loadDmrServerList(network,savedValue){
  var inp=document.getElementById('dmr-server-input');
  var hid=document.getElementById('dmr-server');
  inp.placeholder='Loading...';inp.value='';
  if(hid)hid.value='';
  openrig.getServers(network).then(function(d){
    var servers=d.servers||[];
    combos['dmr-server'].populate(servers,savedValue);
    inp.placeholder='Search servers...';
  }).catch(function(e){console.error('getServers('+network+') failed:',e);inp.placeholder='Search servers...';});
}
function updateEffectiveDmrId(){
  var base=document.getElementById('dmr-id').value.trim();
  var suffix=parseInt(document.getElementById('dmr-id-suffix').value)||0;
  var el=document.getElementById('dmr-id-effective');
  if(!base){el.textContent='';return;}
  if(suffix>0){
    el.textContent='Effective hotspot ID: '+base+String(suffix).padStart(2,'0');
  }else{
    el.textContent='Effective hotspot ID: '+base;
  }
}
function onDmrNetworkChange(){loadDmrServerList(document.getElementById('dmr-network').value,'');}
function loadYsfList(network,inputId,savedValue){
  var inp=document.getElementById(inputId+'-input');
  inp.placeholder='Loading...';inp.value='';
  openrig.getServers(network).then(function(d){
    var items=d.servers||[];
    var labels=d.labels||[];
    combos[inputId].populate(items,savedValue,labels);
    inp.placeholder='Search...';
  }).catch(function(e){console.error('getServers('+network+') failed:',e);inp.placeholder='Search...';});
}
function onYsfNetworkChange(){
  var n=document.getElementById('ysf-network').value;
  document.getElementById('ysf-reflector-group').style.display=n==='ysf'?'':'none';
  document.getElementById('ysf-fcs-group').style.display=n==='fcs'?'':'none';
  document.getElementById('ysf-custom-group').style.display=n==='custom'?'':'none';
  if(n==='ysf')loadYsfList('ysf','ysf-reflector','');
  else if(n==='fcs')loadYsfList('fcs','fcs-room','');
}
function timeAgo(ts){var d=Math.floor((Date.now()-new Date(ts).getTime())/1000);if(d<60)return d+'s ago';if(d<3600)return Math.floor(d/60)+'m ago';if(d<86400)return Math.floor(d/3600)+'h ago';return Math.floor(d/86400)+'d ago';}
function baseCall(cs){if(!cs)return cs;if(cs.indexOf('/')>=0)cs=cs.split('/').reduce(function(a,b){return a.length>=b.length?a:b;});var d=cs.indexOf('-');return d>=0?cs.slice(0,d):cs;}
var lastHeardEntries=[];
var heardMap=null;
var heardMarker=null;      // single pin — always the latest (or pinned) callsign
var heardCallsignInfo={};  // call -> QRZ info
var pinnedCallsign=null;   // null = auto-follow latest
function initHeardMap(){
  if(heardMap)return;
  heardMap=L.map('heard-map',{center:[20,0],zoom:4});
  L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png',{attribution:'&copy; OpenStreetMap contributors',maxZoom:18}).addTo(heardMap);
  // clicking the map background unpins
  heardMap.on('click',function(){pinnedCallsign=null;});
}
function updateMapPin(info,fly){
  if(!heardMap||!info||!info.lat||!info.lon)return;
  var call=info.call;
  var popup='<a class="map-call-link" href="https://www.qrz.com/db/'+encodeURIComponent(call)+'" target="_blank">'+esc(call)+'</a>'
    +(info.firstName||info.lastName?' <span style="color:#94a3b8">'+esc((info.firstName||'')+' '+(info.lastName||'')).trim()+'</span>':'')
    +(info.city||info.state||info.country?'<br><span style="color:#64748b;font-size:.85rem">'+esc([info.city,info.state,info.country].filter(Boolean).join(', '))+'</span>':'')
    +(info.grid?'<br><span style="color:#64748b;font-size:.85rem">'+esc(info.grid)+'</span>':'');
  if(heardMarker){
    heardMarker.setLatLng([info.lat,info.lon]).setPopupContent(popup);
  } else {
    heardMarker=L.marker([info.lat,info.lon]).addTo(heardMap).bindPopup(popup);
    heardMarker.on('click',function(ev){
      L.DomEvent.stopPropagation(ev);
      pinnedCallsign=call;
      showHeardDetail(heardCallsignInfo[call]);
      heardMap.flyTo([info.lat,info.lon],6,{duration:1.5});
    });
  }
  if(fly)heardMap.flyTo([info.lat,info.lon],6,{duration:1.5});
}
function showHeardDetail(info){
  var d=document.getElementById('heard-detail');
  if(!info){d.style.display='none';return;}
  d.style.display='flex';
  var img=document.getElementById('hd-img');
  if(info.imageUrl){img.src=info.imageUrl;img.style.display='';}else{img.style.display='none';}
  var callEl=document.getElementById('hd-call');
  callEl.textContent=(info.heardAs&&info.heardAs!==info.call)?info.heardAs:info.call||'';
  callEl.href='https://www.qrz.com/db/'+encodeURIComponent(info.call||'');
  document.getElementById('hd-name').textContent=((info.firstName||'')+' '+(info.lastName||'')).trim();
  document.getElementById('hd-loc').textContent=[info.city,info.state,info.country].filter(Boolean).join(', ');
  document.getElementById('hd-grid').textContent=info.grid?'Grid: '+info.grid:'';
  document.getElementById('hd-class').textContent=info.licenseClass?'Class: '+info.licenseClass:'';
}
function makeHeardRow(e){
  var tr=document.createElement('tr');
  tr.dataset.ts=e.timestamp;
  tr.dataset.arrived=e.arrivedAt||Date.now(); // browser epoch ms — immune to server/browser clock skew
  tr.dataset.call=e.callsign;
  tr.dataset.mode=e.mode;
  var mc=e.mode==='DMR'?'color:#3b82f6':e.mode==='YSF'?'color:#22c55e':'';
  var qrzHref='https://www.qrz.com/db/'+encodeURIComponent(baseCall(e.callsign));
  tr.innerHTML='<td><a href="'+qrzHref+'" target="_blank" class="map-call-link">'+esc(e.callsign)+'</a></td>'
    +'<td style="'+mc+'">'+esc(e.mode)+'</td><td>'+esc(e.info)+'</td>'
    +'<td class="dur-cell">'+esc(e.duration)+'</td>'
    +'<td class="loss-cell">'+esc(e.loss||'')+'</td>'
    +'<td>'+timeAgoMs(tr.dataset.arrived)+'</td>';
  return tr;
}
function timeAgoMs(ms){var d=Math.floor((Date.now()-Number(ms))/1000);if(d<60)return d+'s ago';if(d<3600)return Math.floor(d/60)+'m ago';if(d<86400)return Math.floor(d/3600)+'h ago';return Math.floor(d/86400)+'d ago';}
function findHeardRow(callsign,mode,timestamp){
  var sel=timestamp
    ?'#lastHeardBody tr[data-call="'+CSS.escape(callsign)+'"][data-mode="'+CSS.escape(mode)+'"][data-ts="'+CSS.escape(timestamp)+'"]'
    :'#lastHeardBody tr[data-call="'+CSS.escape(callsign)+'"][data-mode="'+CSS.escape(mode)+'"]';
  return document.querySelector(sel);
}
function appendOrUpdateLastHeard(e){
  var tb=document.getElementById('lastHeardBody');
  var tbl=document.getElementById('lastHeardTable');
  var emp=document.getElementById('lastHeardEmpty');
  // Same transmission — duration update only: patch the cell, no DOM restructure.
  var sameIdx=-1;
  for(var i=0;i<lastHeardEntries.length;i++){
    if(lastHeardEntries[i].callsign===e.callsign&&lastHeardEntries[i].mode===e.mode&&lastHeardEntries[i].timestamp===e.timestamp){sameIdx=i;break;}
  }
  if(sameIdx>=0){
    lastHeardEntries[sameIdx].duration=e.duration;
    lastHeardEntries[sameIdx].loss=e.loss||'';
    var cell=findHeardRow(e.callsign,e.mode,e.timestamp);
    if(cell){
      cell.querySelector('.dur-cell').textContent=e.duration;
      cell.querySelector('.loss-cell').textContent=e.loss||'';
    }
    return;
  }
  // New entry — remove old row for this callsign+mode if present, prepend new row.
  var oldIdx=-1;
  for(var i=0;i<lastHeardEntries.length;i++){if(lastHeardEntries[i].callsign===e.callsign&&lastHeardEntries[i].mode===e.mode){oldIdx=i;break;}}
  if(oldIdx>=0){
    lastHeardEntries.splice(oldIdx,1);
    var oldRow=findHeardRow(e.callsign,e.mode,null);
    if(oldRow)oldRow.parentNode.removeChild(oldRow);
  }
  e.arrivedAt=Date.now();
  lastHeardEntries.unshift(e);
  if(lastHeardEntries.length>50){
    lastHeardEntries.length=50;
    if(tb.rows.length>50)tb.deleteRow(tb.rows.length-1);
  }
  tbl.style.display='';emp.style.display='none';
  tb.insertBefore(makeHeardRow(e),tb.firstChild);
  // Map pin — use cache if available (no fly), otherwise lookup and fly once.
  if(typeof openrig!=='undefined'&&openrig.lookupCallsign){
    var lookupCall=baseCall(e.callsign);
    if(heardCallsignInfo[lookupCall]){
      initHeardMap();
      var cachedInfo=Object.assign({},heardCallsignInfo[lookupCall],{heardAs:e.callsign});
      updateMapPin(cachedInfo,pinnedCallsign===null);
      if(pinnedCallsign===null)showHeardDetail(cachedInfo);
    } else {
      openrig.lookupCallsign(lookupCall).then(function(info){
        if(info&&info.lat&&info.lon){
          initHeardMap();
          heardCallsignInfo[info.call]=info;
          var infoWithOrig=Object.assign({},info,{heardAs:e.callsign});
          updateMapPin(infoWithOrig,pinnedCallsign===null);
          if(pinnedCallsign===null)showHeardDetail(infoWithOrig);
        }
      }).catch(function(){});
    }
  }
}
var talkgroups=[];
var tgNames={};
function buildTGNames(){tgNames={};talkgroups.forEach(function(tg){if(tg.tg)tgNames[tg.tg]=tg.name||'';});}
function updateTGName(){var v=document.getElementById('ysf2dmr-tg').value;var s=document.getElementById('ysf2dmrTGName');if(v&&tgNames[parseInt(v)]!==undefined){s.textContent=tgNames[parseInt(v)];}else{s.textContent='';}}
var modemPorts={'mmdvm_hs_hat':'/dev/ttyAMA0','mmdvm_hs_dual_hat':'/dev/ttyAMA0','zumspot':'/dev/ttyACM0','dvmega':'/dev/ttyACM0','nano_hotspot':'/dev/ttyACM0'};
function onModemTypeChange(){
  var t=document.getElementById('modem-type').value;
  var p=document.getElementById('modem-port');
  if(modemPorts[t])p.value=modemPorts[t];
}
function loadHotspot(){
  openrig.getHotspot().then(function(d){
    var modem=d.modem||{};
    document.getElementById('modem-type').value=modem.type||'mmdvm_hs_hat';
    document.getElementById('modem-port').value=modem.port||'/dev/ttyAMA0';
    document.getElementById('rf-frequency').value=d.rfFrequency?(+d.rfFrequency).toFixed(4):'';
    document.getElementById('tx-frequency').value=d.txFrequency?(+d.txFrequency).toFixed(4):'0.0000';
    document.getElementById('dmr-enabled').checked=d.dmr.enabled;
    document.getElementById('dmr-id').value=d.dmr.dmrId||'';
    document.getElementById('dmr-id-suffix').value=d.dmr.dmrIdSuffix||0;
    updateEffectiveDmrId();
    document.getElementById('dmr-colorcode').value=d.dmr.colorcode||1;
    document.getElementById('dmr-network').value=d.dmr.network||'brandmeister';
    loadDmrServerList(d.dmr.network||'brandmeister',d.dmr.server||'');
    document.getElementById('dmr-password').value=d.dmr.password||'';
    talkgroups=d.dmr.talkgroups||[];
    renderTalkgroups();
    buildTGNames();
    updateTGName();
    document.getElementById('ysf-enabled').checked=d.ysf.enabled;
    document.getElementById('ysf-network').value=d.ysf.network||'ysf';
    var yn=d.ysf.network||'ysf';
    document.getElementById('ysf-reflector-group').style.display=yn==='ysf'?'':'none';
    document.getElementById('ysf-fcs-group').style.display=yn==='fcs'?'':'none';
    document.getElementById('ysf-custom-group').style.display=yn==='custom'?'':'none';
    if(yn==='fcs'){document.getElementById('fcs-module').value=d.ysf.module||'A';loadYsfList('fcs','fcs-room',d.ysf.reflector||'');}
    else if(yn==='custom'){document.getElementById('ysf-custom-server').value=d.ysf.reflector||'';}
    else{loadYsfList('ysf','ysf-reflector',d.ysf.reflector||'');}
    // Link manager dropdowns
    loadYsfList('ysf','lm-ysf-ref',d.ysf.reflector||'');
    document.getElementById('ysf-description').value=d.ysf.description||'';
    document.getElementById('ysf-wiresx-passthrough').checked=d.ysf.wiresxPassthrough||false;
    document.getElementById('ysf2dmr-enabled').checked=d.crossMode.ysf2dmrEnabled;
    document.getElementById('ysf2dmr-tg').value=d.crossMode.ysf2dmrTalkgroup||'';
  }).catch(function(e){console.error('loadHotspot failed:',e);});
}
function renderTalkgroups(){
  var tb=document.getElementById('tg-body');
  if(!talkgroups||talkgroups.length===0){tb.innerHTML='<tr><td colspan="4" class="empty">No talkgroups configured</td></tr>';return;}
  var html='';
  talkgroups.forEach(function(tg,i){
    html+='<tr><td><input type="number" value="'+tg.tg+'" onchange="talkgroups['+i+'].tg=parseInt(this.value);buildTGNames();updateTGName()" style="width:80px"></td>';
    html+='<td><input type="number" value="'+tg.slot+'" min="1" max="2" onchange="talkgroups['+i+'].slot=parseInt(this.value)" style="width:60px"></td>';
    html+='<td><input type="text" value="'+esc(tg.name)+'" onchange="talkgroups['+i+'].name=this.value;buildTGNames();updateTGName()"></td>';
    html+='<td class="tg-actions"><button class="btn-danger" onclick="removeTG('+i+')" style="padding:.3rem .5rem;font-size:.75rem">X</button></td></tr>';
  });
  tb.innerHTML=html;
}
function addTalkgroup(){talkgroups.push({tg:0,slot:1,name:''});renderTalkgroups();}
function removeTG(i){talkgroups.splice(i,1);renderTalkgroups();}
function saveHotspot(){
  var body={
    modem:{type:document.getElementById('modem-type').value,
      port:document.getElementById('modem-port').value},
    rfFrequency:parseFloat(document.getElementById('rf-frequency').value)||0,
    txFrequency:parseFloat(document.getElementById('tx-frequency').value)||0,
    dmr:{enabled:document.getElementById('dmr-enabled').checked,
      dmrId:parseInt(document.getElementById('dmr-id').value)||0,
      dmrIdSuffix:parseInt(document.getElementById('dmr-id-suffix').value)||0,
      colorcode:parseInt(document.getElementById('dmr-colorcode').value)||1,
      network:document.getElementById('dmr-network').value,
      server:document.getElementById('dmr-server').value,
      password:document.getElementById('dmr-password').value,
      talkgroups:talkgroups},
    ysf:(function(){var n=document.getElementById('ysf-network').value;
      var r=n==='fcs'?document.getElementById('fcs-room').value:n==='custom'?document.getElementById('ysf-custom-server').value:document.getElementById('ysf-reflector').value;
      return{enabled:document.getElementById('ysf-enabled').checked,
      network:n,reflector:r,module:n==='fcs'?document.getElementById('fcs-module').value:'',
      description:document.getElementById('ysf-description').value,
      wiresxPassthrough:document.getElementById('ysf-wiresx-passthrough').checked}})(),
    crossMode:{ysf2dmrEnabled:document.getElementById('ysf2dmr-enabled').checked,
      ysf2dmrTalkgroup:parseInt(document.getElementById('ysf2dmr-tg').value)||0,
      dmr2ysfEnabled:false,dmr2ysfRoom:''}
  };
  openrig.updateHotspot(body).then(function(){toast('Hotspot config saved');openrig.getHotspot().then(renderHotspotStatus).catch(function(){});}).catch(function(e){toast(e.message,true);});
}
var wifiNets=[];
function loadWifi(){
  openrig.getWifi().then(function(d){wifiNets=d.networks||[];renderWifiNets();}).catch(function(){});
}
function renderWifiNets(){
  var c=document.getElementById('wifi-nets');
  if(!wifiNets||wifiNets.length===0){c.innerHTML='';return;}
  var html='';
  wifiNets.forEach(function(n,i){
    html+='<div class="net-block"><div class="net-header"><span class="net-label">Network '+(i+1)+(i===0?' - highest priority':'')+'</span>';
    html+='<button class="btn-danger" onclick="removeWifi('+i+')" style="padding:.3rem .6rem;font-size:.8rem">Remove</button></div>';
    html+='<label>SSID</label><input type="text" value="'+esc(n.ssid)+'" onchange="wifiNets['+i+'].ssid=this.value">';
    html+='<label>Password</label><input type="password" placeholder="Min. 8 characters" onchange="wifiNets['+i+'].password=this.value">';
    html+='<label>Security</label><select onchange="wifiNets['+i+'].security=this.value">';
    html+='<option value="auto"'+(n.security==='auto'||!n.security?' selected':'')+'>WPA2 + WPA3 (recommended)</option>';
    html+='<option value="wpa2"'+(n.security==='wpa2'?' selected':'')+'>WPA2 only</option>';
    html+='<option value="wpa3"'+(n.security==='wpa3'?' selected':'')+'>WPA3 only</option>';
    html+='</select></div>';
  });
  c.innerHTML=html;
}
function addWifiNetwork(){if(wifiNets.length>=5)return;wifiNets.push({ssid:'',security:'auto',priority:0,password:''});renderWifiNets();}
function removeWifi(i){wifiNets.splice(i,1);renderWifiNets();}
function saveWifi(){
  var nets=wifiNets.map(function(n,i){return{ssid:n.ssid,password:n.password||'',security:n.security||'auto',priority:wifiNets.length-i};});
  openrig.updateWifi({networks:nets}).then(function(){toast('WiFi config saved');}).catch(function(e){toast(e.message,true);});
}
function onStreamError(n){
  var w=document.getElementById('conn-warn');
  if(n===0){w.style.display='none';return;}
  document.getElementById('conn-warn-count').textContent=n;
  w.style.display='';
}
function restartSvc(name){
  openrig.restartService(name).then(function(){toast(name+' restarted');}).catch(function(e){toast(e.message,true);});
}
function lmRequestChange(mode,action){
  var cmd='';
  if(mode==='ysf'){
    if(action==='unlink'){cmd='UnLink';}
    else{var r=document.getElementById('lm-ysf-ref').value;if(!r){toast('Select a reflector first',true);return;}cmd='LinkYSF '+r;}
  } else {
    cmd=action==='link'?'enable net1':'disable net1';
  }
  fetch('/gateway-cmd',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({gateway:mode,command:cmd})})
    .then(function(r){if(!r.ok)throw new Error('Request failed ('+r.status+')');return r.json();})
    .then(function(){toast(action==='link'?'Link command sent':'Unlink command sent');})
    .catch(function(e){toast((e&&e.message)||'Command failed',true);});
}
function loadDevice(){
  openrig.getConfig().then(function(d){
    document.getElementById('dev-callsign').value=d.callsign||'';
    document.getElementById('dev-hostname').value=d.hostname||'';
    document.getElementById('dev-name').value=d.name||'';
    document.getElementById('dev-grid').value=d.gridSquare||'';
    document.getElementById('dev-timezone').value=d.timezone||'UTC';
    document.getElementById('dev-qrz-user').value=d.qrzUsername||'';
    document.getElementById('dev-qrz-pass').value=d.qrzPassword||'';
  }).catch(function(){});
}
function testQrzCreds(){
  var callsign=document.getElementById('dev-callsign').value||'KC1YGY';
  var res=document.getElementById('qrz-test-result');
  res.style.display='';res.style.color='#94a3b8';res.textContent='Saving credentials…';
  // Save device config first so the lookup uses the credentials currently in the form
  var body={callsign:document.getElementById('dev-callsign').value,hostname:document.getElementById('dev-hostname').value,
    name:document.getElementById('dev-name').value,gridSquare:document.getElementById('dev-grid').value,
    timezone:document.getElementById('dev-timezone').value,
    qrzUsername:document.getElementById('dev-qrz-user').value,
    qrzPassword:document.getElementById('dev-qrz-pass').value};
  openrig.updateConfig(body).then(function(){
    res.textContent='Testing lookup for '+callsign+'…';
    return openrig.lookupCallsign(callsign);
  }).then(function(info){
    var name=((info.firstName||'')+' '+(info.lastName||'')).trim();
    res.style.color='#22c55e';
    res.textContent='OK — '+info.call+(name?' ('+name+')':'')+(info.city?', '+info.city:'')+(info.state?', '+info.state:'');
  }).catch(function(e){
    res.style.color='#ef4444';
    res.textContent='Failed: '+e.message;
  });
}
function saveDevice(){
  var body={callsign:document.getElementById('dev-callsign').value,hostname:document.getElementById('dev-hostname').value,
    name:document.getElementById('dev-name').value,gridSquare:document.getElementById('dev-grid').value,
    timezone:document.getElementById('dev-timezone').value,
    qrzUsername:document.getElementById('dev-qrz-user').value,
    qrzPassword:document.getElementById('dev-qrz-pass').value};
  openrig.updateConfig(body).then(function(){toast('Device config saved');openrig.getStatus().then(renderStatus).catch(function(){});}).catch(function(e){toast(e.message,true);});
}
function esc(s){if(!s)return'';var d=document.createElement('div');d.appendChild(document.createTextNode(s));return d.innerHTML;}
function renderHotspotStatus(d){
  var card=document.getElementById('hs-card');
  if(!card)return;
  card.style.display='';
  var ysfOn=!!(d.ysf&&d.ysf.enabled);
  document.getElementById('lm-ysf-panel').style.display=ysfOn?'':'none';
  document.getElementById('lm-container').style.display=ysfOn?'':'none';
  function mode(id,on){var el=document.getElementById(id);el.className='hs-mode'+(on?' on':'');}
  mode('hs-dmr',d.dmr&&d.dmr.enabled);
  mode('hs-ysf',d.ysf&&d.ysf.enabled);
  mode('hs-ysf2dmr',d.crossMode&&d.crossMode.ysf2dmrEnabled);
  mode('hs-dmr2ysf',d.crossMode&&d.crossMode.dmr2ysfEnabled);
  document.getElementById('hs-freq').textContent=d.rfFrequency?(d.rfFrequency.toFixed(4)+' MHz'):'--';
  if(d.dmr&&d.dmr.enabled){
    document.getElementById('hs-cc').textContent=d.dmr.colorcode||'--';
    document.getElementById('hs-id').textContent=d.dmr.dmrId||'--';
    document.getElementById('hs-cc-row').style.display='';
    document.getElementById('hs-id-row').style.display='';
  } else {
    document.getElementById('hs-cc-row').style.display='none';
    document.getElementById('hs-id-row').style.display='none';
  }
  var netEl=document.getElementById('hs-net');
  if(d.dmr&&d.dmr.enabled&&d.dmr.server){
    document.getElementById('hs-net-hdr').textContent='DMR Network';
    netEl.textContent=d.dmr.server;
    netEl.className='hs-full';
  } else if(d.ysf&&d.ysf.enabled&&d.ysf.reflector){
    document.getElementById('hs-net-hdr').textContent='YSF Reflector';
    netEl.textContent=d.ysf.reflector;
    var ls=d.ysf.linkState||'unlinked';
    netEl.className='hs-full '+(ls==='linking'?'linked':ls==='relinking'?'linking':'unlinked');
  } else {
    document.getElementById('hs-net-hdr').textContent='Network';
    netEl.textContent='--';
    netEl.className='hs-full';
  }
}
function initPage(){
  makeCombo('dmr-server');
  makeCombo('ysf-reflector');
  makeCombo('fcs-room');
  makeCombo('lm-ysf-ref');
  initHeardMap();
  openrig.getStatus().then(renderStatus).catch(function(){});
  openrig.streamStatus(renderStatus,onStreamError);
  openrig.streamLastHeard(appendOrUpdateLastHeard,onStreamError);
  setInterval(function(){document.querySelectorAll('#lastHeardBody tr').forEach(function(tr){var a=tr.dataset.arrived;if(a){tr.cells[tr.cells.length-1].textContent=timeAgoMs(a);}});},30000);
  openrig.getHotspot().then(renderHotspotStatus).catch(function(){});
  setInterval(function(){openrig.getHotspot().then(renderHotspotStatus).catch(function(){});},10000);
  loadHotspot();loadWifi();loadDevice();
  document.getElementById('ysf2dmr-tg').addEventListener('input',updateTGName);
}
var go=new Go();
WebAssembly.instantiateStreaming(fetch('/openrig.wasm'),go.importObject).then(function(result){
  go.run(result.instance);
  (function waitForOpenrig(){
    if(typeof openrig!=='undefined'){initPage();}
    else{setTimeout(waitForOpenrig,10);}
  })();
});
</script>
</body>
</html>`))

func handleHotspotUI(w http.ResponseWriter, r *http.Request) {
	cfg, err := readConfig()
	if err != nil || !isProvisioned(cfg) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	uiTmpl.Execute(w, nil)
}

// ── Main ──────────────────────────────────────────────────────────────────

func main() {
	flag.BoolVar(&devMode, "dev", false, "Run in local dev mode: use ./openrig.json, listen on :8080, stub system calls")
	flag.Parse()

	if devMode {
		configPath = "./openrig.json"
		wpaConfPath = "./wpa_supplicant-dev.conf"
		listenAddr = ":8080"
		// Seed a local config if none exists
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			seed := []byte(`{"openrig":{"device":{"provisioned":false}}}`)
			if err := os.WriteFile(configPath, seed, 0644); err != nil {
				log.Fatalf("Cannot create %s: %v", configPath, err)
			}
			log.Printf("[dev] Created seed config at %s", configPath)
		}
		log.Printf("[dev] Dev mode enabled — config: %s, addr: %s", configPath, listenAddr)
	}

	if _, err := readConfig(); err != nil {
		log.Fatalf("Cannot read %s: %v", configPath, err)
	}

	// Locate wasm_exec.js — in dev mode resolve via GOROOT, in production
	// use the pre-built copy installed alongside the binary.
	var wasmExecPath string
	if devMode {
		goroot := os.Getenv("GOROOT")
		if goroot == "" {
			out, err := exec.Command("go", "env", "GOROOT").Output()
			if err != nil {
				log.Fatalf("Dev mode: cannot determine GOROOT: %v", err)
			}
			goroot = strings.TrimSpace(string(out))
		}
		wasmExecPath = filepath.Join(goroot, "lib", "wasm", "wasm_exec.js")
	} else {
		wasmExecPath = "/usr/local/lib/openrig/wasm_exec.js"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/favicon.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write([]byte(faviconSVG))
	})
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/gateway-cmd", handleGatewayCmd)
	mux.HandleFunc("/scan", handleScan)
	mux.HandleFunc("/provision", handleProvision)
	mux.HandleFunc("/management", handleManagement)
	mux.HandleFunc("/hotspot", handleHotspotUI)
	mux.HandleFunc("/api/", handleAPIProxy)
	// ConnectRPC service paths (used by WASM client)
	mux.HandleFunc("/openrig.v1.DeviceService/", handleAPIProxy)
	mux.HandleFunc("/openrig.v1.HotspotService/", handleAPIProxy)
	mux.HandleFunc("/openrig.v1.WifiService/", handleAPIProxy)
	mux.HandleFunc("/openrig.v1.RigService/", handleAPIProxy)

	// WASM client files
	mux.HandleFunc("/wasm_exec.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		http.ServeFile(w, r, wasmExecPath)
	})
	wasmBinPath := "/usr/local/lib/openrig/openrig.wasm"
	if devMode {
		wasmBinPath = "/tmp/openrig.wasm"
	}
	mux.HandleFunc("/openrig.wasm", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/wasm")
		http.ServeFile(w, r, wasmBinPath)
	})

	log.Printf("openRigOS web UI listening on %s", listenAddr)
	if err := (&http.Server{Addr: listenAddr, Handler: mux}).ListenAndServe(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
