//go:build js && wasm

// Command tollgate-wasm exposes the demo session to the browser. Build with
//
//	GOOS=js GOARCH=wasm go build -o web/public/tollgate.wasm ./cmd/tollgate-wasm
//
// Every function takes and returns JSON strings, so the JavaScript side stays thin.
package main

import (
	"encoding/json"
	"syscall/js"

	"github.com/TECHMINEs/Tollgate/internal/audit"
	"github.com/TECHMINEs/Tollgate/internal/demo"
	"github.com/TECHMINEs/Tollgate/internal/sim"
)

var session *demo.Session

func reply(v any, err error) any {
	if err != nil {
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(b)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return `{"error":"encode failed"}`
	}
	return string(b)
}

func config(arg js.Value) (sim.Config, error) {
	cfg := sim.DefaultConfig()
	if arg.Type() == js.TypeString && arg.String() != "" {
		if err := json.Unmarshal([]byte(arg.String()), &cfg); err != nil {
			return cfg, err
		}
	}
	if cfg.Start.IsZero() {
		cfg.Start = sim.DefaultConfig().Start
	}
	cfg.ShipmentsPerTenant = min(max(cfg.ShipmentsPerTenant, 1), 200)
	return cfg, nil
}

func main() {
	api := map[string]func(args []js.Value) any{
		"start": func(a []js.Value) any {
			cfg, err := config(a[0])
			if err != nil {
				return reply(nil, err)
			}
			session = demo.NewSession(cfg, nil)
			return reply(session.Meta(), nil)
		},
		"step": func(a []js.Value) any {
			return reply(session.Step(a[0].Int()), nil)
		},
		"dashboard": func(a []js.Value) any {
			return reply(session.Dashboard(a[0].String(), a[1].Int()))
		},
		"shipment": func(a []js.Value) any {
			return reply(session.Shipment(a[0].String(), a[1].String()))
		},
		"whatIf": func(a []js.Value) any {
			cfg, err := config(a[0])
			if err != nil {
				return reply(nil, err)
			}
			var pol audit.Policy
			if err := json.Unmarshal([]byte(a[2].String()), &pol); err != nil {
				return reply(nil, err)
			}
			return reply(demo.WhatIf(cfg, a[1].String(), pol))
		},
		"replayCheck": func(a []js.Value) any {
			cfg, err := config(a[0])
			if err != nil {
				return reply(nil, err)
			}
			return reply(demo.ReplayCheck(cfg), nil)
		},
	}
	obj := js.Global().Get("Object").New()
	for name, fn := range api {
		fn := fn
		obj.Set(name, js.FuncOf(func(_ js.Value, args []js.Value) any { return fn(args) }))
	}
	js.Global().Set("tollgate", obj)
	js.Global().Call("dispatchEvent", js.Global().Get("Event").New("tollgate-ready"))
	select {}
}
