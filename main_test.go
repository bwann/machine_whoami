package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// loadFixture reads a JSON fixture file from testdata/
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("failed to read fixture %s: %v", name, err)
	}
	return data
}

// testConfig returns an AppConfig suitable for tests
func testConfig(t *testing.T) *AppConfig {
	t.Helper()
	return &AppConfig{
		OutputDir:      t.TempDir(),
		Domain:         "example.com",
		NetboxURL:      "https://netbox.example.com",
		NetboxToken:    "fake-token",
		DeviceRoles:    []string{"router", "server", "switch", "workstation"},
		DeviceStatuses: []string{"active", "offline", "staged", "failed", "decommissioning"},
	}
}

// --- Fixture parsing tests ---
// These catch Netbox API structure changes (renamed/moved/removed fields).

func TestParseDeviceResponse(t *testing.T) {
	data := loadFixture(t, "devices.json")

	var resp NetboxResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("failed to unmarshal devices fixture: %v", err)
	}

	if resp.Count != 5 {
		t.Errorf("expected count 5, got %d", resp.Count)
	}
	if len(resp.Results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(resp.Results))
	}

	// Verify first device (kn-sw4.ok1) has all critical fields populated
	dev := resp.Results[0]
	checks := []struct {
		field string
		got   string
		want  string
	}{
		{"Name", dev.Name, "kn-sw4.ok1"},
		{"Status.Value", dev.Status.Value, "active"},
		{"Status.Label", dev.Status.Label, "Active"},
		{"Role.Slug", dev.Role.Slug, "switch"},
		{"Site.Slug", dev.Site.Slug, "lab-1"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("device %s: got %q, want %q", c.field, c.got, c.want)
		}
	}
	if dev.Site.ID != 10 {
		t.Errorf("device Site.ID: got %d, want 10", dev.Site.ID)
	}
	if dev.Rack == nil {
		t.Fatal("device Rack is nil, expected non-nil")
	}
	if dev.Rack.ID != 100 {
		t.Errorf("device Rack.ID: got %d, want 100", dev.Rack.ID)
	}
	if dev.PrimaryIP4 == nil {
		t.Fatal("device PrimaryIP4 is nil")
	}
	if dev.PrimaryIP4.Address != "10.1.2.3/24" {
		t.Errorf("device PrimaryIP4.Address: got %q, want %q", dev.PrimaryIP4.Address, "10.1.2.3/24")
	}
	if dev.PrimaryIP6 == nil {
		t.Fatal("device PrimaryIP6 is nil")
	}
	if dev.PrimaryIP6.Address != "2001:db8:1:123::1/64" {
		t.Errorf("device PrimaryIP6.Address: got %q, want %q", dev.PrimaryIP6.Address, "2001:db8:1:123::1/64")
	}

	// Verify device with no rack (nuc01)
	dev2 := resp.Results[1]
	if dev2.Rack != nil {
		t.Errorf("nuc01 should have nil Rack, got %+v", dev2.Rack)
	}

	// Verify device with empty name
	dev4 := resp.Results[3]
	if dev4.Name != "" {
		t.Errorf("device 4 Name: got %q, want empty", dev4.Name)
	}
	if dev4.ID != 4 {
		t.Errorf("device 4 ID: got %d, want 4", dev4.ID)
	}
}

func TestParseDeviceResponseLegacyRole(t *testing.T) {
	data := loadFixture(t, "devices_legacy.json")

	var resp NetboxResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("failed to unmarshal legacy devices fixture: %v", err)
	}

	dev := resp.Results[0]

	// role field should be empty (old Netbox)
	if dev.Role.Slug != "" {
		t.Errorf("legacy device Role.Slug should be empty, got %q", dev.Role.Slug)
	}

	// device_role should be populated
	if dev.DeviceRole.Slug != "switch" {
		t.Errorf("legacy device DeviceRole.Slug: got %q, want %q", dev.DeviceRole.Slug, "switch")
	}

	// RoleSlug() should fall back to device_role
	if dev.RoleSlug() != "switch" {
		t.Errorf("legacy device RoleSlug(): got %q, want %q", dev.RoleSlug(), "switch")
	}
}

func TestParseSiteResponse(t *testing.T) {
	data := loadFixture(t, "sites.json")

	var resp struct {
		Next    *string      `json:"next"`
		Results []NetboxSite `json:"results"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("failed to unmarshal sites fixture: %v", err)
	}

	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 sites, got %d", len(resp.Results))
	}

	site := resp.Results[0]
	if site.ID != 10 {
		t.Errorf("site ID: got %d, want 10", site.ID)
	}
	if site.Slug != "lab-1" {
		t.Errorf("site Slug: got %q, want %q", site.Slug, "lab-1")
	}
	if site.Region == nil {
		t.Fatal("site Region is nil")
	}
	if site.Region.Slug != "oklahoma" {
		t.Errorf("site Region.Slug: got %q, want %q", site.Region.Slug, "oklahoma")
	}
}

func TestParseRackResponse(t *testing.T) {
	data := loadFixture(t, "racks.json")

	var resp struct {
		Next    *string      `json:"next"`
		Results []NetboxRack `json:"results"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("failed to unmarshal racks fixture: %v", err)
	}

	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 racks, got %d", len(resp.Results))
	}

	rack := resp.Results[0]
	if rack.ID != 100 {
		t.Errorf("rack ID: got %d, want 100", rack.ID)
	}
	if rack.Name != "Rack-A1" {
		t.Errorf("rack Name: got %q, want %q", rack.Name, "Rack-A1")
	}

	// Verify BGP_ASN custom field is accessible
	asn := extractRackASN(&rack)
	if asn != "65001" {
		t.Errorf("extractRackASN: got %q, want %q", asn, "65001")
	}
}

func TestParseRackResponseNoASN(t *testing.T) {
	rack := &NetboxRack{
		ID:           999,
		Name:         "Empty-Rack",
		CustomFields: map[string]interface{}{},
	}
	asn := extractRackASN(rack)
	if asn != "" {
		t.Errorf("extractRackASN with no BGP_ASN: got %q, want empty", asn)
	}
}

// --- Transformation tests ---
// These catch bugs in the logic that converts API data into output.

func TestBuildDeviceInfo_FullyPopulated(t *testing.T) {
	cfg := &AppConfig{Domain: "example.com"}
	siteMap := map[int]*NetboxSite{
		10: {ID: 10, Slug: "lab-1", Region: &struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		}{ID: 1, Name: "Oklahoma", Slug: "oklahoma"}},
	}
	rackMap := map[int]*NetboxRack{
		100: {ID: 100, Name: "Rack-A1", CustomFields: map[string]interface{}{"BGP_ASN": float64(65001)}},
	}

	pos := float64(12)
	dev := NetboxDevice{
		ID:   1,
		Name: "kn-sw4.ok1",
	}
	dev.Status.Value = "active"
	dev.Status.Label = "Active"
	dev.Role.Slug = "switch"
	dev.Site.ID = 10
	dev.Site.Slug = "lab-1"
	dev.Rack = &struct {
		Name string `json:"name"`
		ID   int    `json:"id"`
	}{Name: "Rack-A1", ID: 100}
	dev.Position = &pos
	dev.PrimaryIP4 = &NetboxIPAddress{Address: "10.1.2.3/24", ID: 501}
	dev.PrimaryIP6 = &NetboxIPAddress{Address: "2001:db8:1:123::1/64", ID: 502}

	info := buildDeviceInfo(cfg, dev, siteMap, rackMap)

	checks := []struct {
		field string
		got   string
		want  string
	}{
		{"FQDN", info.FQDN, "kn-sw4.ok1.example.com"},
		{"Region", info.Region, "oklahoma"},
		{"Datacenter", info.Datacenter, "lab-1"},
		{"Cluster", info.Cluster, ""},
		{"Rack", info.Rack, "Rack-A1"},
		{"Position", info.Position, "12"},
		{"ASN", info.ASN, "65001"},
		{"Subnet4", info.Subnet4, "10.1.2.0/24"},
		{"Subnet6", info.Subnet6, "2001:db8:1:123::/64"},
		{"Status", info.Status, "Active"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("DeviceInfo.%s: got %q, want %q", c.field, c.got, c.want)
		}
	}
}

func TestBuildDeviceInfo_NoRack(t *testing.T) {
	cfg := &AppConfig{Domain: "example.com"}
	siteMap := map[int]*NetboxSite{
		20: {ID: 20, Slug: "ok1", Region: &struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		}{ID: 1, Name: "Oklahoma", Slug: "oklahoma"}},
	}
	rackMap := map[int]*NetboxRack{}

	dev := NetboxDevice{ID: 2, Name: "nuc01.ok1"}
	dev.Status.Label = "Active"
	dev.Site.ID = 20
	dev.Site.Slug = "ok1"
	dev.PrimaryIP4 = &NetboxIPAddress{Address: "10.1.2.10/24", ID: 503}

	info := buildDeviceInfo(cfg, dev, siteMap, rackMap)

	if info.Rack != "" {
		t.Errorf("Rack should be empty, got %q", info.Rack)
	}
	if info.Position != "" {
		t.Errorf("Position should be empty, got %q", info.Position)
	}
	if info.ASN != "" {
		t.Errorf("ASN should be empty, got %q", info.ASN)
	}
	if info.Subnet4 != "10.1.2.0/24" {
		t.Errorf("Subnet4: got %q, want %q", info.Subnet4, "10.1.2.0/24")
	}
	if info.Subnet6 != "" {
		t.Errorf("Subnet6 should be empty, got %q", info.Subnet6)
	}
}

func TestBuildDeviceInfo_IPv6Only(t *testing.T) {
	cfg := &AppConfig{Domain: "example.com"}

	dev := NetboxDevice{ID: 6, Name: "v6host.ok1"}
	dev.Status.Label = "Active"
	dev.Site.ID = 20
	dev.Site.Slug = "ok1"
	dev.PrimaryIP6 = &NetboxIPAddress{Address: "2001:db8:2:1::10/64", ID: 507}

	info := buildDeviceInfo(cfg, dev, map[int]*NetboxSite{}, map[int]*NetboxRack{})

	if info.Subnet4 != "" {
		t.Errorf("Subnet4 should be empty, got %q", info.Subnet4)
	}
	if info.Subnet6 != "2001:db8:2:1::/64" {
		t.Errorf("Subnet6: got %q, want %q", info.Subnet6, "2001:db8:2:1::/64")
	}
}

func TestBuildDeviceInfo_NoSiteInMap(t *testing.T) {
	cfg := &AppConfig{Domain: "example.com"}
	siteMap := map[int]*NetboxSite{} // empty - site not found
	rackMap := map[int]*NetboxRack{}

	dev := NetboxDevice{ID: 99, Name: "orphan-host"}
	dev.Status.Label = "Active"
	dev.Site.ID = 999
	dev.Site.Slug = "unknown-site"

	info := buildDeviceInfo(cfg, dev, siteMap, rackMap)

	if info.Region != "" {
		t.Errorf("Region should be empty for unknown site, got %q", info.Region)
	}
	if info.Datacenter != "unknown-site" {
		t.Errorf("Datacenter: got %q, want %q", info.Datacenter, "unknown-site")
	}
}

// --- Golden file tests ---
// These catch unexpected changes to the output file format.

func TestWriteDeviceFile_Golden(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &AppConfig{OutputDir: tmpDir, Domain: "example.com"}

	siteMap := map[int]*NetboxSite{
		10: {ID: 10, Slug: "lab-1", Region: &struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		}{ID: 1, Name: "Oklahoma", Slug: "oklahoma"}},
		20: {ID: 20, Slug: "ok1", Region: &struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		}{ID: 1, Name: "Oklahoma", Slug: "oklahoma"}},
	}
	rackMap := map[int]*NetboxRack{
		100: {ID: 100, Name: "Rack-A1", CustomFields: map[string]interface{}{"BGP_ASN": float64(65001)}},
	}

	tests := []struct {
		name       string
		device     NetboxDevice
		goldenFile string
	}{
		{
			name:       "fully populated device",
			goldenFile: "golden_kn-sw4.ok1.example.com.txt",
			device: func() NetboxDevice {
				pos := float64(12)
				dev := NetboxDevice{ID: 1, Name: "kn-sw4.ok1"}
				dev.Status.Label = "Active"
				dev.Site.ID = 10
				dev.Site.Slug = "lab-1"
				dev.Rack = &struct {
					Name string `json:"name"`
					ID   int    `json:"id"`
				}{Name: "Rack-A1", ID: 100}
				dev.Position = &pos
				dev.PrimaryIP4 = &NetboxIPAddress{Address: "10.1.2.3/24", ID: 501}
				dev.PrimaryIP6 = &NetboxIPAddress{Address: "2001:db8:1:123::1/64", ID: 502}
				return dev
			}(),
		},
		{
			name:       "device with no rack",
			goldenFile: "golden_nuc01.ok1.example.com.txt",
			device: func() NetboxDevice {
				dev := NetboxDevice{ID: 2, Name: "nuc01.ok1"}
				dev.Status.Label = "Active"
				dev.Site.ID = 20
				dev.Site.Slug = "ok1"
				dev.PrimaryIP4 = &NetboxIPAddress{Address: "10.1.2.10/24", ID: 503}
				return dev
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := buildDeviceInfo(cfg, tt.device, siteMap, rackMap)
			if _, err := writeDeviceFile(cfg, info, time.Unix(1700000000, 0)); err != nil {
				t.Fatalf("writeDeviceFile failed: %v", err)
			}

			// Read written file
			outPath := filepath.Join(tmpDir, info.Datacenter, info.FQDN+".txt")
			got, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatalf("failed to read output file: %v", err)
			}

			// Compare against golden file
			want, err := os.ReadFile(filepath.Join("testdata", tt.goldenFile))
			if err != nil {
				t.Fatalf("failed to read golden file: %v", err)
			}

			if string(got) != string(want) {
				t.Errorf("output does not match golden file %s:\n--- got ---\n%s\n--- want ---\n%s",
					tt.goldenFile, string(got), string(want))
			}
		})
	}
}

func TestWriteDeviceFileOnlyOnChange(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &AppConfig{OutputDir: tmpDir}
	info := &DeviceInfo{FQDN: "host1.ok1.example.com", Datacenter: "ok1", Status: "Active"}
	path := filepath.Join(tmpDir, "ok1", "host1.ok1.example.com.txt")
	t1, t2 := time.Unix(1700000000, 0), time.Unix(1700000600, 0)

	readFile := func() string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return string(b)
	}

	// First write creates the file with the first timestamp
	written, err := writeDeviceFile(cfg, info, t1)
	if err != nil || !written {
		t.Fatalf("first write: written=%v err=%v", written, err)
	}
	first := readFile()
	if !strings.HasSuffix(first, "DEVICE_WHOAMI_GENERATED_AT=1700000000\n") {
		t.Fatalf("missing/incorrect timestamp:\n%s", first)
	}
	st1, _ := os.Stat(path)

	// Same data on a later run: no write, timestamp and inode preserved
	written, err = writeDeviceFile(cfg, info, t2)
	if err != nil || written {
		t.Fatalf("unchanged write: written=%v err=%v", written, err)
	}
	if got := readFile(); got != first {
		t.Errorf("unchanged file was modified:\n%s", got)
	}
	st2, _ := os.Stat(path)
	if !os.SameFile(st1, st2) {
		t.Error("unchanged file was replaced (inode changed)")
	}

	// Changed data: rewritten with the new timestamp
	info.Status = "Offline"
	written, err = writeDeviceFile(cfg, info, t2)
	if err != nil || !written {
		t.Fatalf("changed write: written=%v err=%v", written, err)
	}
	got := readFile()
	if !strings.Contains(got, `DEVICE_STATUS="Offline"`) || !strings.HasSuffix(got, "DEVICE_WHOAMI_GENERATED_AT=1700000600\n") {
		t.Errorf("changed file not rewritten correctly:\n%s", got)
	}

	// File from an older version (no timestamp line) gets upgraded
	body, _ := stripGeneratedAt(got)
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	written, err = writeDeviceFile(cfg, info, t1)
	if err != nil || !written {
		t.Fatalf("legacy upgrade: written=%v err=%v", written, err)
	}
	if !strings.HasSuffix(readFile(), "DEVICE_WHOAMI_GENERATED_AT=1700000000\n") {
		t.Errorf("legacy file not stamped:\n%s", readFile())
	}
}

// --- Unit tests for helper functions ---

func TestEnsureFQDN(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		domain string
		want   string
	}{
		{"already fully qualified", "host1.ok1.example.com", "example.com", "host1.ok1.example.com"},
		{"has dots but not our domain", "nuc01.ok1", "example.com", "nuc01.ok1.example.com"},
		{"trailing dot", "nuc01.ok1.", "example.com", "nuc01.ok1.example.com"},
		{"bare hostname", "switch1", "example.com", "switch1.example.com"},
		{"different domain", "host.example.org", "example.com", "host.example.org.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ensureFQDN(tt.input, tt.domain)
			if got != tt.want {
				t.Errorf("ensureFQDN(%q, %q) = %q, want %q", tt.input, tt.domain, got, tt.want)
			}
		})
	}
}

func TestExtractSubnet(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"ipv4 /24", "10.1.2.3/24", "10.1.2.0/24"},
		{"ipv4 /30", "172.16.0.1/30", "172.16.0.0/30"},
		{"ipv4 /32", "192.168.1.1/32", "192.168.1.1/32"},
		{"ipv6 /64", "2001:db8:1:123::1/64", "2001:db8:1:123::/64"},
		{"ipv6 /48", "2001:db8:1::1/48", "2001:db8:1::/48"},
		{"invalid", "not-an-ip", ""},
		{"no mask", "10.1.2.3", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSubnet(tt.input)
			if got != tt.want {
				t.Errorf("extractSubnet(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRoleSlug(t *testing.T) {
	// Netbox 3.6+ (role populated)
	dev := NetboxDevice{}
	dev.Role.Slug = "server"
	dev.DeviceRole.Slug = "old-server"
	if got := dev.RoleSlug(); got != "server" {
		t.Errorf("RoleSlug() with both fields: got %q, want %q", got, "server")
	}

	// Older Netbox (only device_role)
	dev2 := NetboxDevice{}
	dev2.DeviceRole.Slug = "switch"
	if got := dev2.RoleSlug(); got != "switch" {
		t.Errorf("RoleSlug() with only device_role: got %q, want %q", got, "switch")
	}

	// Neither set
	dev3 := NetboxDevice{}
	if got := dev3.RoleSlug(); got != "" {
		t.Errorf("RoleSlug() with neither: got %q, want empty", got)
	}
}

func TestParseSiteInfo(t *testing.T) {
	dc, cluster := parseSiteInfo("lab-1")
	if dc != "lab-1" {
		t.Errorf("parseSiteInfo datacenter: got %q, want %q", dc, "lab-1")
	}
	if cluster != "" {
		t.Errorf("parseSiteInfo cluster: got %q, want empty", cluster)
	}
}

func TestExtractRackASN(t *testing.T) {
	// BGP_ASN as float64 (how JSON numbers unmarshal)
	rack := &NetboxRack{CustomFields: map[string]interface{}{"BGP_ASN": float64(65001)}}
	if got := extractRackASN(rack); got != "65001" {
		t.Errorf("extractRackASN float: got %q, want %q", got, "65001")
	}

	// BGP_ASN as string
	rack2 := &NetboxRack{CustomFields: map[string]interface{}{"BGP_ASN": "65002"}}
	if got := extractRackASN(rack2); got != "65002" {
		t.Errorf("extractRackASN string: got %q, want %q", got, "65002")
	}

	// BGP_ASN is nil
	rack3 := &NetboxRack{CustomFields: map[string]interface{}{"BGP_ASN": nil}}
	if got := extractRackASN(rack3); got != "" {
		t.Errorf("extractRackASN nil: got %q, want empty", got)
	}

	// No custom fields at all
	rack4 := &NetboxRack{CustomFields: map[string]interface{}{}}
	if got := extractRackASN(rack4); got != "" {
		t.Errorf("extractRackASN missing: got %q, want empty", got)
	}
}
