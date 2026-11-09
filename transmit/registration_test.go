package transmit_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/transmit"
)

func TestReadRegistrationPreflightSharesSendValidation(t *testing.T) {
	mutations := map[string]func(*transmit.Config){
		"normal":             func(*transmit.Config) {},
		"outside origin":     func(c *transmit.Config) { c.Resources[0].URL = "http://other.invalid/document" },
		"not read only":      func(c *transmit.Config) { c.Resources[0].ReadOnly = false },
		"unknown account":    func(c *transmit.Config) { c.Resources[0].Accounts = []string{"unregistered"} },
		"duplicate session":  func(c *transmit.Config) { c.Sessions = append(c.Sessions, c.Sessions[0]) },
		"duplicate resource": func(c *transmit.Config) { c.Resources = append(c.Resources, c.Resources[0]) },
		"no status path":     func(c *transmit.Config) { c.Resources[0].View.StatusPath = nil },
		"ambiguous status":   func(c *transmit.Config) { c.Resources[0].View.ErrorValues = []string{"ok"} },
		"duplicate marker field": func(c *transmit.Config) {
			c.Resources[0].View.Markers = append(c.Resources[0].View.Markers, c.Resources[0].View.Markers[0])
		},
		"deep path": func(c *transmit.Config) {
			c.Resources[0].View.DataPath = strings.Split(strings.Repeat("data/", 33)+"data", "/")
		},
		"forbidden header": func(c *transmit.Config) { c.Sessions[0].Headers.Set("Host", "other.invalid") },
	}
	for name, change := range mutations {
		t.Run(name, func(t *testing.T) {
			cfg := e2FixtureConfig("http://127.0.0.1:1", &e2Recorder{})
			change(&cfg)
			before, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			preflight := transmit.CheckReadRegistration(cfg.Origin, cfg.Sessions, cfg.Resources)
			tx, actual := transmit.NewHTTP(context.Background(), cfg)
			if tx != nil {
				tx.Stop()
			}
			if (preflight == nil) != (name == "normal") || (actual == nil) != (preflight == nil) {
				t.Fatal("preparation and actual constructor disagree")
			}
			after, err := json.Marshal(cfg)
			if err != nil || string(before) != string(after) {
				t.Fatal("registration validation mutated input")
			}
		})
	}
}
