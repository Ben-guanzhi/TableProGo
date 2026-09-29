package main

import "testing"

func TestDatabaseFilterNeedsNoSwitch(t *testing.T) {
	empty := DatabaseFilter{}
	if !empty.allows("master") {
		t.Error("a filter without patterns must show everything")
	}

	include := DatabaseFilter{Include: []string{"zs", "*_000008"}}
	for _, name := range []string{"zs", "ZS", "UFTData867476_000008"} {
		if !include.allows(name) {
			t.Errorf("%s should match %v", name, include.Include)
		}
	}
	for _, name := range []string{"master", "zsarchive", "UFTData_0000081"} {
		if include.allows(name) {
			t.Errorf("%s should not match %v", name, include.Include)
		}
	}

	excludeWins := DatabaseFilter{Include: []string{"zs*"}, Exclude: []string{"zs_temp"}}
	if excludeWins.allows("zs_temp") {
		t.Error("exclude must win over include")
	}
	if !excludeWins.allows("zs_env") {
		t.Error("included database was hidden")
	}

	excludeOnly := DatabaseFilter{Exclude: []string{"tempdb"}}
	if !excludeOnly.allows("zs") {
		t.Error("an empty include list keeps everything not excluded")
	}
}
