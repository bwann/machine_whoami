// Copyright 2026 Bryan Wann
// SPDX-License-Identifier: Apache-2.0
//
// machine_whoami generates per-machine identity files from Netbox.
//
// Example Netbox permissions needed:
// Permissions:
//   whoami_ro:
//     - view
//    - all object types except user*
// User:
//   - whoami_service
//   - Permissions: whoami_ro
// API token:
//   - user whoami_service
//   - write enabled: no
//   - expires: no

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

type AppConfig struct {
	OutputDir      string   `yaml:"output_dir"`
	Domain         string   `yaml:"domain"`
	NetboxURL      string   `yaml:"netbox_url"`
	NetboxToken    string   `yaml:"netbox_token"`
	DeviceRoles    []string `yaml:"device_roles"`
	DeviceStatuses []string `yaml:"device_statuses"`
	RequestTimeout int      `yaml:"request_timeout"` // seconds, default 120
	FetchWorkers   int      `yaml:"fetch_workers"`   // concurrent API fetchers, default 4
}

func loadConfig(path string) (*AppConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening config: %w", err)
	}
	defer f.Close()

	var cfg AppConfig
	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	// Environment variable overrides config file for the token.
	if tok := os.Getenv("NETBOX_API_TOKEN"); tok != "" {
		cfg.NetboxToken = tok
	}

	if cfg.NetboxURL == "" {
		return nil, fmt.Errorf("netbox_url is required")
	}
	if cfg.NetboxToken == "" {
		return nil, fmt.Errorf("netbox_token is required (set in config or NETBOX_API_TOKEN env var)")
	}
	if cfg.OutputDir == "" {
		return nil, fmt.Errorf("output_dir is required")
	}
	if cfg.Domain == "" {
		return nil, fmt.Errorf("default domain to build FQDNs are required")
	}
	if len(cfg.DeviceRoles) == 0 {
		return nil, fmt.Errorf("device_roles must list at least one role")
	}
	if len(cfg.DeviceStatuses) == 0 {
		return nil, fmt.Errorf("device_statuses must list at least one status")
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 120
	}
	if cfg.FetchWorkers <= 0 {
		cfg.FetchWorkers = 4
	}

	return &cfg, nil
}

type NetboxDevice struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Status struct {
		Value string `json:"value"`
		Label string `json:"label"`
	} `json:"status"`
	// Netbox 3.6+ uses "role"; older versions use "device_role"
	Role struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"role"`
	DeviceRole struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"device_role"`
	Site struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"site"`
	Rack *struct {
		Name string `json:"name"`
		ID   int    `json:"id"`
	} `json:"rack"`
	Position     *float64               `json:"position"`
	PrimaryIP4   *NetboxIPAddress       `json:"primary_ip4"`
	PrimaryIP6   *NetboxIPAddress       `json:"primary_ip6"`
	Tags         []NetboxTag            `json:"tags"`
	CustomFields map[string]interface{} `json:"custom_fields"`
}

type NetboxIPAddress struct {
	Address string `json:"address"`
	ID      int    `json:"id"`
}

type NetboxTag struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type NetboxSite struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Slug   string `json:"slug"`
	Region *struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"region"`
}

type NetboxRack struct {
	ID           int                    `json:"id"`
	Name         string                 `json:"name"`
	Tags         []NetboxTag            `json:"tags"`
	CustomFields map[string]interface{} `json:"custom_fields"`
}

type NetboxResponse struct {
	Count   int            `json:"count"`
	Next    *string        `json:"next"`
	Results []NetboxDevice `json:"results"`
}

type DeviceInfo struct {
	FQDN       string
	Region     string
	Datacenter string
	Cluster    string
	Rack       string
	Position   string
	ASN        string
	Subnet4    string
	Subnet6    string
	Status     string
}

// RoleSlug returns the device role slug, handling both Netbox 3.6+ ("role")
// and older API versions ("device_role").
func (d *NetboxDevice) RoleSlug() string {
	if d.Role.Slug != "" {
		return d.Role.Slug
	}
	return d.DeviceRole.Slug
}

func main() {
	var configPath string
	var regionsFlag string
	flag.StringVar(&configPath, "config", "machine_whoami.yaml", "Path to config file")
	flag.StringVar(&regionsFlag, "regions", "", "Comma-separated list of regions to process")
	flag.Parse()

	appCfg, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	startTime := time.Now()
	client := &http.Client{Timeout: time.Duration(appCfg.RequestTimeout) * time.Second}

	if err := os.MkdirAll(appCfg.OutputDir, 0755); err != nil {
		log.Fatalf("Failed to create output directory: %v", err)
	}

	var regionFilter []string
	if regionsFlag != "" {
		regionFilter = strings.Split(regionsFlag, ",")
		for i := range regionFilter {
			regionFilter[i] = strings.TrimSpace(regionFilter[i])
		}
		log.Printf("Filtering for regions: %v", regionFilter)
	}

	// Pre-fetch all sites and racks so workers don't make per-device API calls
	siteMap, err := fetchAllSites(client, appCfg)
	if err != nil {
		log.Fatalf("Failed to fetch sites: %v", err)
	}

	rackMap, err := fetchAllRacks(client, appCfg)
	if err != nil {
		log.Fatalf("Failed to fetch racks: %v", err)
	}

	devices, err := fetchAllDevices(client, appCfg, siteMap, regionFilter)
	if err != nil {
		log.Fatalf("Failed to fetch devices: %v", err)
	}

	// Build lookup sets from config for filtering
	allowedRoles := make(map[string]bool, len(appCfg.DeviceRoles))
	for _, r := range appCfg.DeviceRoles {
		allowedRoles[strings.ToLower(r)] = true
	}
	allowedStatuses := make(map[string]bool, len(appCfg.DeviceStatuses))
	for _, s := range appCfg.DeviceStatuses {
		allowedStatuses[strings.ToLower(s)] = true
	}

	// Filter devices by name, role, and status
	var queued []NetboxDevice
	var skippedNoName, skippedRole, skippedStatus int
	for _, device := range devices {
		if device.Name == "" {
			log.Printf("Skipping device ID %d: no name set", device.ID)
			skippedNoName++
			continue
		}
		if !allowedRoles[strings.ToLower(device.RoleSlug())] {
			skippedRole++
			continue
		}
		if !allowedStatuses[strings.ToLower(device.Status.Value)] {
			skippedStatus++
			continue
		}
		queued = append(queued, device)
	}

	log.Printf("Processing %d devices (fetched %d, skipped: %d no name, %d wrong role, %d wrong status)",
		len(queued), len(devices), skippedNoName, skippedRole, skippedStatus)

	// Track which files we've written for cleanup
	writtenFiles := make(map[string]map[string]bool) // datacenter -> filename -> true
	var writtenMutex sync.Mutex

	// Track successes and failures
	var succeeded, unchanged atomic.Int64
	var failures []string
	var failMutex sync.Mutex

	jobs := make(chan NetboxDevice, 1000)

	const numWriters = 20
	var wg sync.WaitGroup

	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for device := range jobs {
				info := buildDeviceInfo(appCfg, device, siteMap, rackMap)

				written, err := writeDeviceFile(appCfg, info, startTime)
				if err != nil {
					msg := fmt.Sprintf("device %q (ID %d): %v", device.Name, device.ID, err)
					log.Printf("Error: %s", msg)
					failMutex.Lock()
					failures = append(failures, msg)
					failMutex.Unlock()
					continue
				}

				succeeded.Add(1)
				if !written {
					unchanged.Add(1)
				}

				writtenMutex.Lock()
				if writtenFiles[info.Datacenter] == nil {
					writtenFiles[info.Datacenter] = make(map[string]bool)
				}
				writtenFiles[info.Datacenter][info.FQDN+".txt"] = true
				writtenMutex.Unlock()
			}
		}()
	}

	for _, device := range queued {
		jobs <- device
	}
	close(jobs)
	wg.Wait()

	// Summary
	log.Printf("Summary: %d succeeded (%d written, %d unchanged), %d failed out of %d processed",
		succeeded.Load(), succeeded.Load()-unchanged.Load(), unchanged.Load(), len(failures), len(queued))

	// Write failures to file (or clean up old failure log if none)
	failPath := filepath.Join(appCfg.OutputDir, "failures.log")
	if len(failures) > 0 {
		if err := os.WriteFile(failPath, []byte(strings.Join(failures, "\n")+"\n"), 0644); err != nil {
			log.Printf("Error writing failure log: %v", err)
		} else {
			log.Printf("Failures written to %s", failPath)
		}
	} else {
		os.Remove(failPath) // clean up any previous failure log
	}

	// Clean up old files in processed datacenters
	if err := cleanupOldFiles(appCfg, writtenFiles); err != nil {
		log.Printf("Error during cleanup: %v", err)
	}

	log.Printf("machine_whoami generation complete in %.1f seconds", time.Since(startTime).Seconds())
}

func fetchAllDevices(client *http.Client, appCfg *AppConfig, siteMap map[int]*NetboxSite, regionFilter []string) ([]NetboxDevice, error) {
	firstURL := fmt.Sprintf("%s/api/dcim/devices/?limit=1000", appCfg.NetboxURL)
	firstResp, err := makeNetboxRequest(client, appCfg.NetboxToken, firstURL)
	if err != nil {
		return nil, err
	}

	var firstPage NetboxResponse
	if err := json.Unmarshal(firstResp, &firstPage); err != nil {
		return nil, fmt.Errorf("failed to parse first page: %w", err)
	}

	if firstPage.Next == nil {
		return filterDevicesByRegion(firstPage.Results, siteMap, regionFilter), nil
	}

	pageSize := len(firstPage.Results)
	if pageSize == 0 {
		pageSize = 1000
	}
	totalPages := (firstPage.Count + pageSize - 1) / pageSize
	remainingPages := totalPages - 1

	workers := min(appCfg.FetchWorkers, remainingPages)
	log.Printf("Fetching %d device pages with %d workers", totalPages, workers)

	type pageResult struct {
		devices []NetboxDevice
		err     error
	}

	results := make(chan pageResult, remainingPages)
	urlQueue := make(chan string, remainingPages)

	for i := 0; i < workers; i++ {
		go func() {
			for url := range urlQueue {
				resp, err := makeNetboxRequest(client, appCfg.NetboxToken, url)
				if err != nil {
					results <- pageResult{err: err}
					continue
				}
				var page NetboxResponse
				if err := json.Unmarshal(resp, &page); err != nil {
					results <- pageResult{err: fmt.Errorf("failed to parse page: %w", err)}
					continue
				}
				results <- pageResult{devices: page.Results}
			}
		}()
	}

	// Queue offset-based URLs for all remaining pages
	go func() {
		for offset := pageSize; offset < firstPage.Count; offset += pageSize {
			urlQueue <- fmt.Sprintf("%s/api/dcim/devices/?limit=%d&offset=%d",
				appCfg.NetboxURL, pageSize, offset)
		}
		close(urlQueue)
	}()

	allDevices := make([]NetboxDevice, 0, firstPage.Count)
	allDevices = append(allDevices, firstPage.Results...)

	var fetchErrors []error
	for i := 0; i < remainingPages; i++ {
		result := <-results
		if result.err != nil {
			fetchErrors = append(fetchErrors, result.err)
			continue
		}
		allDevices = append(allDevices, result.devices...)
	}

	if len(fetchErrors) > 0 {
		return nil, fmt.Errorf("encountered %d errors during fetch: %v", len(fetchErrors), fetchErrors[0])
	}

	log.Printf("Fetched %d devices total", len(allDevices))
	return filterDevicesByRegion(allDevices, siteMap, regionFilter), nil
}

func fetchAllSites(client *http.Client, appCfg *AppConfig) (map[int]*NetboxSite, error) {
	siteMap := make(map[int]*NetboxSite)
	url := fmt.Sprintf("%s/api/dcim/sites/?limit=1000", appCfg.NetboxURL)

	for url != "" {
		resp, err := makeNetboxRequest(client, appCfg.NetboxToken, url)
		if err != nil {
			return nil, err
		}

		var page struct {
			Next    *string      `json:"next"`
			Results []NetboxSite `json:"results"`
		}
		if err := json.Unmarshal(resp, &page); err != nil {
			return nil, fmt.Errorf("failed to parse sites: %w", err)
		}

		for i := range page.Results {
			s := &page.Results[i]
			siteMap[s.ID] = s
		}

		if page.Next != nil {
			url = *page.Next
		} else {
			url = ""
		}
	}

	log.Printf("Fetched %d sites", len(siteMap))
	return siteMap, nil
}

func fetchAllRacks(client *http.Client, appCfg *AppConfig) (map[int]*NetboxRack, error) {
	rackMap := make(map[int]*NetboxRack)
	url := fmt.Sprintf("%s/api/dcim/racks/?limit=1000", appCfg.NetboxURL)

	for url != "" {
		resp, err := makeNetboxRequest(client, appCfg.NetboxToken, url)
		if err != nil {
			return nil, err
		}

		var page struct {
			Next    *string      `json:"next"`
			Results []NetboxRack `json:"results"`
		}
		if err := json.Unmarshal(resp, &page); err != nil {
			return nil, fmt.Errorf("failed to parse racks: %w", err)
		}

		for i := range page.Results {
			r := &page.Results[i]
			rackMap[r.ID] = r
		}

		if page.Next != nil {
			url = *page.Next
		} else {
			url = ""
		}
	}

	log.Printf("Fetched %d racks", len(rackMap))
	return rackMap, nil
}

func filterDevicesByRegion(devices []NetboxDevice, siteMap map[int]*NetboxSite, regionFilter []string) []NetboxDevice {
	if len(regionFilter) == 0 {
		return devices
	}

	filtered := make([]NetboxDevice, 0, len(devices))
	for _, device := range devices {
		site, ok := siteMap[device.Site.ID]
		if !ok || site.Region == nil {
			continue
		}
		for _, allowedRegion := range regionFilter {
			if strings.EqualFold(site.Region.Slug, allowedRegion) ||
				strings.EqualFold(site.Region.Name, allowedRegion) {
				filtered = append(filtered, device)
				break
			}
		}
	}

	log.Printf("Filtered to %d devices for regions: %v", len(filtered), regionFilter)
	return filtered
}

func makeNetboxRequest(client *http.Client, token string, url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Token %s", token))
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Netbox API returned status %d fetching '%s': %s", resp.StatusCode, url, string(body))
	}

	return io.ReadAll(resp.Body)
}

func buildDeviceInfo(appCfg *AppConfig, device NetboxDevice, siteMap map[int]*NetboxSite, rackMap map[int]*NetboxRack) *DeviceInfo {
	info := &DeviceInfo{
		FQDN:   ensureFQDN(device.Name, appCfg.Domain),
		Status: device.Status.Label,
	}

	// Look up full site to get region (nested site in device API omits region)
	if site, ok := siteMap[device.Site.ID]; ok {
		if site.Region != nil {
			info.Region = site.Region.Slug
		}
	}
	info.Datacenter, info.Cluster = parseSiteInfo(device.Site.Slug)

	if device.Rack != nil {
		info.Rack = device.Rack.Name
		if device.Position != nil {
			info.Position = fmt.Sprintf("%.0f", *device.Position)
		}
		if rack, ok := rackMap[device.Rack.ID]; ok {
			info.ASN = extractRackASN(rack)
		}
	}

	// Subnets of the device's primary IPv4 and IPv6 addresses
	if device.PrimaryIP4 != nil {
		info.Subnet4 = extractSubnet(device.PrimaryIP4.Address)
	}
	if device.PrimaryIP6 != nil {
		info.Subnet6 = extractSubnet(device.PrimaryIP6.Address)
	}

	return info
}

// generatedAtKey records when a device file's content last changed. It is
// excluded when comparing old and new content, so a device whose Netbox data
// never changes keeps the same file (and timestamp) for its whole lifetime.
const generatedAtKey = "DEVICE_WHOAMI_GENERATED_AT"

// writeDeviceFile writes the device file only if its content differs from what
// is already on disk. It reports whether the file was written.
func writeDeviceFile(appCfg *AppConfig, info *DeviceInfo, now time.Time) (bool, error) {
	dcDir := filepath.Join(appCfg.OutputDir, info.Datacenter)
	filename := filepath.Join(dcDir, fmt.Sprintf("%s.txt", info.FQDN))
	tempFilename := filename + ".tmp"

	body := fmt.Sprintf(`DEVICE_NAME="%s"
DEVICE_REGION="%s"
DEVICE_DATACENTER="%s"
DEVICE_CLUSTER="%s"
DEVICE_RACK="%s"
DEVICE_RACK_POSITION="%s"
DEVICE_ASN="%s"
DEVICE_SUBNET4="%s"
DEVICE_SUBNET6="%s"
DEVICE_STATUS="%s"
`,
		info.FQDN,
		info.Region,
		info.Datacenter,
		info.Cluster,
		info.Rack,
		info.Position,
		info.ASN,
		info.Subnet4,
		info.Subnet6,
		info.Status,
	)

	if existing, err := os.ReadFile(filename); err == nil {
		if oldBody, hasStamp := stripGeneratedAt(string(existing)); hasStamp && oldBody == body {
			return false, nil
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("failed to read existing file: %w", err)
	}

	if err := os.MkdirAll(dcDir, 0755); err != nil {
		return false, fmt.Errorf("failed to create datacenter directory: %w", err)
	}

	content := body + fmt.Sprintf("%s=%d\n", generatedAtKey, now.Unix())

	if err := os.WriteFile(tempFilename, []byte(content), 0644); err != nil {
		return false, fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tempFilename, filename); err != nil {
		os.Remove(tempFilename)
		return false, fmt.Errorf("failed to rename temp file: %w", err)
	}

	return true, nil
}

// stripGeneratedAt removes the generated-at line from file content, reporting
// whether one was present. Files without it (written by older versions) are
// treated as changed so they pick up a timestamp.
func stripGeneratedAt(content string) (string, bool) {
	var b strings.Builder
	found := false
	for _, line := range strings.SplitAfter(content, "\n") {
		if strings.HasPrefix(line, generatedAtKey+"=") {
			found = true
			continue
		}
		b.WriteString(line)
	}
	return b.String(), found
}

func cleanupOldFiles(appCfg *AppConfig, writtenFiles map[string]map[string]bool) error {
	dcDirs, err := os.ReadDir(appCfg.OutputDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read output directory: %w", err)
	}

	for _, dcEntry := range dcDirs {
		if !dcEntry.IsDir() {
			continue
		}

		datacenter := dcEntry.Name()
		dcPath := filepath.Join(appCfg.OutputDir, datacenter)

		files, err := os.ReadDir(dcPath)
		if err != nil {
			log.Printf("Warning: failed to read datacenter directory %s: %v", datacenter, err)
			continue
		}

		// If we didn't write any files for this datacenter in this run, skip cleanup
		// (we may be filtering by region and this DC is in a different region)
		if writtenFiles[datacenter] == nil {
			continue
		}

		for _, file := range files {
			if file.IsDir() {
				continue
			}

			filename := file.Name()

			if strings.HasSuffix(filename, ".tmp") {
				continue
			}

			if !writtenFiles[datacenter][filename] {
				filePath := filepath.Join(dcPath, filename)
				if err := os.Remove(filePath); err != nil {
					log.Printf("Warning: failed to remove old file %s: %v", filePath, err)
				} else {
					log.Printf("Removed old file: %s", filePath)
				}
			}
		}
	}

	return nil
}

// BGP_ASN is a custom field applied to Rack objects in the case of datacenter
// L3 BGP network topolgies where every top-of-rack switch has its own ASN
func extractRackASN(rack *NetboxRack) string {
	if asn, ok := rack.CustomFields["BGP_ASN"]; ok && asn != nil {
		return fmt.Sprintf("%v", asn)
	}
	return ""
}

// Sometimes people are inconsistent about whether or not they gave fully-qualified
// hostnames to devices, do our best to make FQDNs.
func ensureFQDN(name, domain string) string {
	if strings.HasSuffix(name, "."+domain) {
		return name
	}

	if strings.Contains(name, ".") {
		name = strings.TrimSuffix(name, ".")
		return name + "." + domain
	}

	return name + "." + domain
}

func parseSiteInfo(siteSlug string) (datacenter, cluster string) {
	// Datacenter is the site slug directly.
        // TODO: handle 'cluster', this will probably be a custom field someday
	return siteSlug, ""
}

// If our Netbox data was really good we'd have per-rack VLANs with subnets assigned
// that we could just pull, but in the meantime just derive it from the device
// primary IP addresses
func extractSubnet(ipAddress string) string {
	// net.ParseCIDR returns the network address with host bits zeroed.
	_, network, err := net.ParseCIDR(ipAddress)
	if err != nil {
		return ""
	}
	return network.String()
}
