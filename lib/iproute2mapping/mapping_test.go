package iproute2mapping

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetMappingFromText(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		wantId   int
		wantName string
		wantErr  bool
	}{
		{name: "tab", text: "254\tmain", wantId: 254, wantName: "main"},
		{name: "space", text: "254 main", wantId: 254, wantName: "main"},
		{name: "many spaces", text: "254     main", wantId: 254, wantName: "main"},
		{name: "mixed whitespace", text: "254 \t main", wantId: 254, wantName: "main"},
		{name: "trailing comment", text: "100 mytable # my table", wantId: 100, wantName: "mytable"},
		{name: "hex", text: "0x10 hextable", wantId: 16, wantName: "hextable"},
		{name: "leading zeros are decimal", text: "010 oct", wantId: 10, wantName: "oct"},
		{name: "only a number", text: "254", wantErr: true},
		{name: "only a name", text: "main", wantErr: true},
		{name: "name before number", text: "main 254", wantErr: true},
		{name: "negative", text: "-1 neg", wantErr: true},
		{name: "too large", text: "99999999999 big", wantErr: true},
		{name: "empty hex", text: "0x hex", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, name, err := getMappingFromText(tt.text)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && (id != tt.wantId || name != tt.wantName) {
				t.Errorf("got (%d, %q), want (%d, %q)", id, name, tt.wantId, tt.wantName)
			}
		})
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoad(t *testing.T) {
	usr, etc := t.TempDir(), t.TempDir()

	writeFile(t, filepath.Join(usr, "rt_tables"),
		"# comment\n\n255\tlocal\n254 main\n253   default\n0\tunspec\n")
	writeFile(t, filepath.Join(usr, "rt_tables.d", "extra.conf"), "200 extra\n")
	writeFile(t, filepath.Join(usr, "rt_tables.d", "ignored.txt"), "201 ignored\n")
	// etc overrides usr
	writeFile(t, filepath.Join(etc, "rt_tables"), "  100  custom  \n254 mainoverride\n")
	writeFile(t, filepath.Join(etc, "rt_tables.d", "more.conf"), "101 more\n")

	m := load([]string{usr, etc}, "rt_tables")
	if len(m.errs) != 0 {
		t.Errorf("unexpected errors: %v", m.errs)
	}

	wantByName := map[string]int{
		"local": 255, "main": 254, "default": 253, "unspec": 0,
		"extra": 200, "custom": 100, "mainoverride": 254, "more": 101,
	}
	if !maps.Equal(m.byName, wantByName) {
		t.Errorf("byName = %v, want %v", m.byName, wantByName)
	}
	if m.byId[254] != "mainoverride" || m.byId[100] != "custom" || m.byId[200] != "extra" {
		t.Errorf("byId = %v", m.byId)
	}
}

func TestLoadMissingRootIsFine(t *testing.T) {
	usr := t.TempDir()
	writeFile(t, filepath.Join(usr, "rt_protos"), "2 kernel\n")

	m := load([]string{usr, filepath.Join(usr, "does-not-exist")}, "rt_protos")
	if len(m.errs) != 0 {
		t.Errorf("unexpected errors: %v", m.errs)
	}
	if m.byName["kernel"] != 2 {
		t.Errorf("byName = %v", m.byName)
	}
}

func TestLoadNothingFound(t *testing.T) {
	m := load([]string{filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")}, "rt_tables")
	if len(m.errs) != 1 || !strings.Contains(m.errs[0].Error(), "no mapping-file") {
		t.Errorf("errs = %v", m.errs)
	}
}

func TestLoadMalformedLinesAreSkipped(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "rt_tables"), "254\nfoo bar\n100 ok\n300\n")

	m := load([]string{dir}, "rt_tables")
	if len(m.errs) != 3 {
		t.Errorf("want 3 errors, got %v", m.errs)
	}
	if !maps.Equal(m.byName, map[string]int{"ok": 100}) {
		t.Errorf("byName = %v", m.byName)
	}
}

func TestTryGetId(t *testing.T) {
	origByName := ByName
	t.Cleanup(func() { ByName = origByName })
	ByName = map[int]map[string]int{TABLE: {"main": 254}}

	tests := []struct {
		name    string
		input   string
		want    int
		wantErr bool
	}{
		{name: "name", input: "main", want: 254},
		{name: "number", input: "100", want: 100},
		{name: "negative number", input: "-1", want: -1},
		{name: "unknown name", input: "nope", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := TryGetId(TABLE, tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}
