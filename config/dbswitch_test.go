/*
 * Copyright © 2026 Joseph Quigley.
 *
 * This file is part of WriteFreely.
 *
 * WriteFreely is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, included
 * in the LICENSE file in this source code package.
 */

package config

import "testing"

// Switching engines on an existing config moves the port to the new engine's
// default only when it still holds the old engine's default.
func TestSwitchEngineKeepsPortSensible(t *testing.T) {
	tests := []struct {
		name     string
		from     string
		port     int
		use      func(*Config, bool)
		wantType string
		wantPort int
	}{
		{"mysql default to postgres", "mysql", 3306, (*Config).UsePostgres, "postgres", 5432},
		{"postgres default to mysql", "postgres", 5432, (*Config).UseMySQL, "mysql", 3306},
		{"custom port kept for postgres", "mysql", 3307, (*Config).UsePostgres, "postgres", 3307},
		{"custom port kept for mysql", "postgres", 6432, (*Config).UseMySQL, "mysql", 6432},
		{"postgres default kept for postgres", "postgres", 5432, (*Config).UsePostgres, "postgres", 5432},
		{"mysql default kept for mysql", "mysql", 3306, (*Config).UseMySQL, "mysql", 3306},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := New()
			cfg.Database.Type = tt.from
			cfg.Database.Host = "db.example"
			cfg.Database.Port = tt.port
			tt.use(cfg, false)
			if cfg.Database.Type != tt.wantType {
				t.Errorf("type = %q, want %q", cfg.Database.Type, tt.wantType)
			}
			if cfg.Database.Port != tt.wantPort {
				t.Errorf("port = %d, want %d", cfg.Database.Port, tt.wantPort)
			}
			if cfg.Database.Host != "db.example" {
				t.Errorf("host = %q, want it left alone", cfg.Database.Host)
			}
		})
	}
}
