package cli

import (
	"strings"
	"testing"
)

func TestParseSubcommands(t *testing.T) {
	cases := []struct {
		argv []string
		act  Action
	}{
		{[]string{"run"}, ActRun},
		{[]string{}, ActRun}, // default run
		{[]string{"configure"}, ActConfigure},
		{[]string{"install", "--iso", "x.iso"}, ActInstall},
		{[]string{"install", "--iso=x.iso"}, ActInstall},
		{[]string{"commit"}, ActCommit},
		{[]string{"help"}, ActHelp},
		{[]string{"--help"}, ActHelp},
		{[]string{"-h"}, ActHelp},
		{[]string{"run", "--help"}, ActHelp},
		{[]string{"--snapshot"}, ActRun}, // flag come primo argomento
	}
	for _, c := range cases {
		a, err := Parse(c.argv)
		if err != nil {
			t.Fatalf("Parse(%v): err=%v", c.argv, err)
		}
		if a.Action != c.act {
			t.Errorf("Parse(%v): action=%v, atteso %v", c.argv, a.Action, c.act)
		}
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"bogus"}, "comando sconosciuto"},
		{[]string{"install"}, "install richiede --iso"},
		{[]string{"configure", "--snapshot"}, "vale solo per run"},
		{[]string{"run", "--iso", "x"}, "vale solo per install"},
		{[]string{"run", "--nope"}, "flag sconosciuto"},
		{[]string{"run", "extra"}, "argomento inatteso"},
		{[]string{"run", "--set"}, "--set richiede"},
		{[]string{"run", "--set", "soloKey"}, "atteso k=v"},
	}
	for _, c := range cases {
		a, err := Parse(c.argv)
		if err == nil {
			t.Errorf("Parse(%v): atteso errore, ok (action=%v)", c.argv, a.Action)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("Parse(%v): err=%q, atteso contenente %q", c.argv, err, c.want)
		}
	}
}

func TestParseFlags(t *testing.T) {
	a, err := Parse([]string{"run", "--config", "custom.toml", "--snapshot",
		"--set", "net=nat", "--set", "AIO=native", "--quiet"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ConfigPath != "custom.toml" {
		t.Errorf("ConfigPath=%q", a.ConfigPath)
	}
	if !a.Snapshot || !a.Quiet {
		t.Errorf("snapshot/quiet non settati: %+v", a)
	}
	if len(a.Set) != 2 || a.Set[0] != (KeyVal{"net", "nat"}) || a.Set[1].Key != "AIO" {
		t.Errorf("Set=%v", a.Set)
	}

	a, err = Parse([]string{"run", "--config=c.toml", "--set=mem=8192"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ConfigPath != "c.toml" || len(a.Set) != 1 || a.Set[0].Key != "mem" {
		t.Errorf("config=',%v set=%v", a.ConfigPath, a.Set)
	}
}

func TestUsageNonEmpty(t *testing.T) {
	var b strings.Builder
	Usage(&b)
	if !strings.Contains(b.String(), "configure") || !strings.Contains(b.String(), "commit") {
		t.Errorf("usage incompleto")
	}
}
