//go:build js && wasm

package main

import (
	"context"
	"net/http"
	"syscall/js"
	"time"

	"connectrpc.com/connect"
	openrigv1 "openrig/gen/openrig/v1"
	"openrig/gen/openrig/v1/openrigv1connect"
	"google.golang.org/protobuf/encoding/protojson"
)

var (
	deviceClient  openrigv1connect.DeviceServiceClient
	hotspotClient openrigv1connect.HotspotServiceClient
	wifiClient    openrigv1connect.WifiServiceClient
	rigClient     openrigv1connect.RigServiceClient
)

var jsonOpts = protojson.MarshalOptions{EmitUnpopulated: true}

func main() {
	baseURL := js.Global().Get("location").Get("origin").String()
	httpClient := &http.Client{}

	deviceClient = openrigv1connect.NewDeviceServiceClient(httpClient, baseURL)
	hotspotClient = openrigv1connect.NewHotspotServiceClient(httpClient, baseURL)
	wifiClient = openrigv1connect.NewWifiServiceClient(httpClient, baseURL)
	rigClient = openrigv1connect.NewRigServiceClient(httpClient, baseURL)

	js.Global().Set("openrig", js.ValueOf(map[string]any{
		"getStatus":       js.FuncOf(jsGetStatus),
		"streamStatus":    js.FuncOf(jsStreamStatus),
		"getConfig":       js.FuncOf(jsGetConfig),
		"updateConfig":    js.FuncOf(jsUpdateConfig),
		"restartService":  js.FuncOf(jsRestartService),
		"reboot":          js.FuncOf(jsReboot),
		"shutdown":        js.FuncOf(jsShutdown),
		"getHotspot":      js.FuncOf(jsGetHotspot),
		"updateHotspot":   js.FuncOf(jsUpdateHotspot),
		"updateDmrId":     js.FuncOf(jsUpdateDmrId),
		"getServers":       js.FuncOf(jsGetServers),
		"lookupCallsign":   js.FuncOf(jsLookupCallsign),
		"streamLastHeard":  js.FuncOf(jsStreamLastHeard),
		"getWifi":         js.FuncOf(jsGetWifi),
		"updateWifi":      js.FuncOf(jsUpdateWifi),
		"scanWifi":        js.FuncOf(jsScanWifi),
		"getNetwork":      js.FuncOf(jsGetNetwork),
		"getRigs":         js.FuncOf(jsGetRigs),
		"updateRigs":      js.FuncOf(jsUpdateRigs),
	}))

	select {} // keep alive
}

// jsPromise wraps a Go function as a JS Promise.
func jsPromise(fn func() ([]byte, error)) js.Value {
	handler := js.FuncOf(func(_ js.Value, args []js.Value) any {
		resolve := args[0]
		reject := args[1]
		go func() {
			b, err := fn()
			if err != nil {
				reject.Invoke(js.Global().Get("Error").New(err.Error()))
				return
			}
			parsed := js.Global().Get("JSON").Call("parse", string(b))
			resolve.Invoke(parsed)
		}()
		return nil
	})
	return js.Global().Get("Promise").New(handler)
}

// jsArg converts a JS object argument to a JSON string.
func jsArg(val js.Value) string {
	return js.Global().Get("JSON").Call("stringify", val).String()
}

// --- Device Service ---

func jsGetStatus(_ js.Value, _ []js.Value) any {
	return jsPromise(func() ([]byte, error) {
		resp, err := deviceClient.GetStatus(context.Background(), connect.NewRequest(&openrigv1.Empty{}))
		if err != nil {
			return nil, err
		}
		return jsonOpts.Marshal(resp.Msg)
	})
}

func jsStreamStatus(_ js.Value, args []js.Value) any {
	callback := args[0]
	var onError js.Value
	if len(args) > 1 && args[1].Type() == js.TypeFunction {
		onError = args[1]
	}
	notifyError := func(n int) {
		if onError.Type() == js.TypeFunction {
			onError.Invoke(n)
		}
	}
	go func() {
		failures := 0
		for {
			stream, err := deviceClient.StreamStatus(context.Background(), connect.NewRequest(&openrigv1.Empty{}))
			if err != nil {
				failures++
				if failures > 3 {
					notifyError(failures)
					time.Sleep(10 * time.Second)
				} else {
					time.Sleep(2 * time.Second)
				}
				continue
			}
			if failures > 0 {
				notifyError(0) // signal recovery
			}
			failures = 0
			for stream.Receive() {
				b, err := jsonOpts.Marshal(stream.Msg())
				if err != nil {
					continue
				}
				parsed := js.Global().Get("JSON").Call("parse", string(b))
				callback.Invoke(parsed)
			}
			stream.Close()
			failures++
			if failures > 3 {
				notifyError(failures)
				time.Sleep(10 * time.Second)
			} else {
				time.Sleep(2 * time.Second)
			}
		}
	}()
	return js.Undefined()
}

func jsGetConfig(_ js.Value, _ []js.Value) any {
	return jsPromise(func() ([]byte, error) {
		resp, err := deviceClient.GetConfig(context.Background(), connect.NewRequest(&openrigv1.Empty{}))
		if err != nil {
			return nil, err
		}
		return jsonOpts.Marshal(resp.Msg)
	})
}

func jsUpdateConfig(_ js.Value, args []js.Value) any {
	raw := jsArg(args[0]) // extract synchronously before goroutine
	return jsPromise(func() ([]byte, error) {
		var cfg openrigv1.DeviceConfig
		if err := protojson.Unmarshal([]byte(raw), &cfg); err != nil {
			return nil, err
		}
		resp, err := deviceClient.UpdateConfig(context.Background(),
			connect.NewRequest(&openrigv1.UpdateConfigRequest{Config: &cfg}))
		if err != nil {
			return nil, err
		}
		return jsonOpts.Marshal(resp.Msg)
	})
}

func jsRestartService(_ js.Value, args []js.Value) any {
	name := args[0].String() // extract synchronously before goroutine
	return jsPromise(func() ([]byte, error) {
		resp, err := deviceClient.RestartService(context.Background(),
			connect.NewRequest(&openrigv1.RestartServiceRequest{Service: name}))
		if err != nil {
			return nil, err
		}
		return jsonOpts.Marshal(resp.Msg)
	})
}

func jsReboot(_ js.Value, _ []js.Value) any {
	return jsPromise(func() ([]byte, error) {
		resp, err := deviceClient.Reboot(context.Background(),
			connect.NewRequest(&openrigv1.RebootRequest{}))
		if err != nil {
			return nil, err
		}
		return jsonOpts.Marshal(resp.Msg)
	})
}

func jsShutdown(_ js.Value, _ []js.Value) any {
	return jsPromise(func() ([]byte, error) {
		resp, err := deviceClient.Shutdown(context.Background(),
			connect.NewRequest(&openrigv1.ShutdownRequest{}))
		if err != nil {
			return nil, err
		}
		return jsonOpts.Marshal(resp.Msg)
	})
}

// --- Hotspot Service ---

func jsGetHotspot(_ js.Value, _ []js.Value) any {
	return jsPromise(func() ([]byte, error) {
		resp, err := hotspotClient.GetHotspot(context.Background(), connect.NewRequest(&openrigv1.Empty{}))
		if err != nil {
			return nil, err
		}
		return jsonOpts.Marshal(resp.Msg)
	})
}

func jsUpdateHotspot(_ js.Value, args []js.Value) any {
	raw := jsArg(args[0]) // extract synchronously before goroutine
	return jsPromise(func() ([]byte, error) {
		var cfg openrigv1.HotspotConfig
		if err := protojson.Unmarshal([]byte(raw), &cfg); err != nil {
			return nil, err
		}
		resp, err := hotspotClient.UpdateHotspot(context.Background(),
			connect.NewRequest(&openrigv1.UpdateHotspotRequest{Config: &cfg}))
		if err != nil {
			return nil, err
		}
		return jsonOpts.Marshal(resp.Msg)
	})
}

func jsUpdateDmrId(_ js.Value, args []js.Value) any {
	id := int32(args[0].Int()) // extract synchronously before goroutine
	return jsPromise(func() ([]byte, error) {
		resp, err := hotspotClient.UpdateDmrId(context.Background(),
			connect.NewRequest(&openrigv1.UpdateDmrIdRequest{DmrId: id}))
		if err != nil {
			return nil, err
		}
		return jsonOpts.Marshal(resp.Msg)
	})
}

func jsGetServers(_ js.Value, args []js.Value) any {
	network := args[0].String() // extract synchronously before goroutine
	return jsPromise(func() ([]byte, error) {
		resp, err := hotspotClient.GetServers(context.Background(),
			connect.NewRequest(&openrigv1.GetServersRequest{Network: network}))
		if err != nil {
			return nil, err
		}
		return jsonOpts.Marshal(resp.Msg)
	})
}

func jsLookupCallsign(_ js.Value, args []js.Value) any {
	callsign := args[0].String()
	return jsPromise(func() ([]byte, error) {
		resp, err := hotspotClient.LookupCallsign(context.Background(),
			connect.NewRequest(&openrigv1.LookupCallsignRequest{Callsign: callsign}))
		if err != nil {
			return nil, err
		}
		return jsonOpts.Marshal(resp.Msg)
	})
}

func jsStreamLastHeard(_ js.Value, args []js.Value) any {
	callback := args[0]
	var onError js.Value
	if len(args) > 1 && args[1].Type() == js.TypeFunction {
		onError = args[1]
	}
	notifyError := func(n int) {
		if onError.Type() == js.TypeFunction {
			onError.Invoke(n)
		}
	}
	go func() {
		failures := 0
		for {
			stream, err := hotspotClient.StreamLastHeard(context.Background(), connect.NewRequest(&openrigv1.Empty{}))
			if err != nil {
				failures++
				if failures > 3 {
					notifyError(failures)
					time.Sleep(10 * time.Second)
				} else {
					time.Sleep(2 * time.Second)
				}
				continue
			}
			if failures > 0 {
				notifyError(0) // signal recovery
			}
			failures = 0
			for stream.Receive() {
				b, err := jsonOpts.Marshal(stream.Msg())
				if err != nil {
					continue
				}
				parsed := js.Global().Get("JSON").Call("parse", string(b))
				callback.Invoke(parsed)
			}
			stream.Close()
			failures++
			if failures > 3 {
				notifyError(failures)
				time.Sleep(10 * time.Second)
			} else {
				time.Sleep(2 * time.Second)
			}
		}
	}()
	return js.Undefined()
}

// --- Wifi Service ---

func jsGetWifi(_ js.Value, _ []js.Value) any {
	return jsPromise(func() ([]byte, error) {
		resp, err := wifiClient.GetWifi(context.Background(), connect.NewRequest(&openrigv1.Empty{}))
		if err != nil {
			return nil, err
		}
		return jsonOpts.Marshal(resp.Msg)
	})
}

func jsUpdateWifi(_ js.Value, args []js.Value) any {
	raw := jsArg(args[0]) // extract synchronously before goroutine
	return jsPromise(func() ([]byte, error) {
		var cfg openrigv1.WifiConfig
		if err := protojson.Unmarshal([]byte(raw), &cfg); err != nil {
			return nil, err
		}
		resp, err := wifiClient.UpdateWifi(context.Background(),
			connect.NewRequest(&openrigv1.UpdateWifiRequest{Config: &cfg}))
		if err != nil {
			return nil, err
		}
		return jsonOpts.Marshal(resp.Msg)
	})
}

func jsScanWifi(_ js.Value, _ []js.Value) any {
	return jsPromise(func() ([]byte, error) {
		resp, err := wifiClient.ScanWifi(context.Background(), connect.NewRequest(&openrigv1.Empty{}))
		if err != nil {
			return nil, err
		}
		return jsonOpts.Marshal(resp.Msg)
	})
}

func jsGetNetwork(_ js.Value, _ []js.Value) any {
	return jsPromise(func() ([]byte, error) {
		resp, err := wifiClient.GetNetwork(context.Background(), connect.NewRequest(&openrigv1.Empty{}))
		if err != nil {
			return nil, err
		}
		return jsonOpts.Marshal(resp.Msg)
	})
}

// --- Rig Service ---

func jsGetRigs(_ js.Value, _ []js.Value) any {
	return jsPromise(func() ([]byte, error) {
		resp, err := rigClient.GetRigs(context.Background(), connect.NewRequest(&openrigv1.Empty{}))
		if err != nil {
			return nil, err
		}
		return jsonOpts.Marshal(resp.Msg)
	})
}

func jsUpdateRigs(_ js.Value, args []js.Value) any {
	raw := jsArg(args[0]) // extract synchronously before goroutine
	return jsPromise(func() ([]byte, error) {
		var req openrigv1.UpdateRigsRequest
		if err := protojson.Unmarshal([]byte(raw), &req); err != nil {
			return nil, err
		}
		resp, err := rigClient.UpdateRigs(context.Background(), connect.NewRequest(&req))
		if err != nil {
			return nil, err
		}
		return jsonOpts.Marshal(resp.Msg)
	})
}
