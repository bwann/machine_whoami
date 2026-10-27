# Netbox /etc/machine_whoami generator

Creates per-machine whoami identity files for inclusion in scripts and
configuration management, sourced from system data in Netbox. Each host's file
is intended to end up on that host as e.g. `/etc/machine_whoami`.

It pulls a select list of fields from Netbox for each host and writes these to
a Bash-compatible plain text file.

`machine_whoami` is intended to run from a central or regional host periodically.
It writes all output files for a set of hosts in bulk to a local directory,
suitable to be served up via HTTP server and further pulled to individual hosts
using configuration management (e.g. Chef).

This de-coupling is done to ensure local consumers of whoami data on hosts
(scripts, configuration management, etc) can continue on in the event of
network partition, configuration management systems unavailable, or the
`machine_whoami` process itself is not working.

The whoami files are expected to rarely, if ever, change for the lifecycle of
the server. But if changes are made in Netbox to a system's location or network,
the changes are faithfully updated in whoami.

Example uses:

* Source directly into Bash scripts with `source /etc/machine_whoami`
* Region/datacenter/rack aware operations, e.g. build my DC/regional foobar server hostname
* Labeling Kubernetes nodes with per-rack BGP AS numbers to consume with Cilium
* Disable starting services when Netbox device status is something other than 'Active'

## Output format

One file per device at `{output_dir}/{datacenter}/{fqdn}.txt`, in
bash-sourceable `NAME=VALUE` form:

```
DEVICE_NAME="web8976.02.tst1.example.com"
DEVICE_REGION="california"
DEVICE_DATACENTER="tst1"
DEVICE_CLUSTER=""
DEVICE_RACK="fg"
DEVICE_RACK_POSITION="18"
DEVICE_ASN="64518"
DEVICE_SUBNET4="10.1.2.0/24"
DEVICE_SUBNET6="2001:db8:1:123::/64"
DEVICE_STATUS="Active"
DEVICE_WHOAMI_GENERATED_AT=1700000000
```

Files are only rewritten when a device's Netbox data changes. If nothing
changes, a device's file stays byte-for-byte identical for its lifetime.
`DEVICE_WHOAMI_GENERATED_AT` is the Unix epoch time at which the file's
content last changed, not the time of the most recent run.

| Field | Netbox source |
|---|---|
| `DEVICE_NAME` | Device name; the configured `domain` is appended if not already present |
| `DEVICE_REGION` | Region slug of the device's site |
| `DEVICE_DATACENTER` | Site slug |
| `DEVICE_CLUSTER` | Currently always empty (reserved for future use) |
| `DEVICE_RACK` | Rack name |
| `DEVICE_RACK_POSITION` | Rack unit position |
| `DEVICE_ASN` | Custom field named `BGP_ASN` on the device's `Rack` object |
| `DEVICE_SUBNET4` | Network of the device's primary IPv4 address (e.g. `10.1.2.3/24` becomes `10.1.2.0/24`) |
| `DEVICE_SUBNET6` | Network of the device's primary IPv6 address |
| `DEVICE_STATUS` | Device status label (e.g. `Active`) |

Fields with no value in Netbox are written as empty strings.

When a device is removed from Netbox (or no longer matches the configured
roles/statuses), its file is deleted on the next run. Devices with no name in
Netbox are skipped and logged by device ID.

## Getting started

You'll need to set up a user and read-only API key in Netbox for `machine_whoami`
to consume data from. Suggested permissions:

- Permissions:
  - Name: `whoami_service_ro`
  - 'Can view'
  - Object types: all except user*
- User:
  - Name: `whoami_service`
  - Assigned permissions: `whoami_service_ro`
- API token:
  - User: `whoami_service`
  - Write enabled: no
  - Expires: no

Next to build `machine_whoami` directly, just run `go build`. There are package
metadata files in this repo to create RPM/DEB packages too along with systemd
unit files.

### Configuration file

This contains information on where to write output files, Netbox URL and API token,
which Netbox device role slugs (`device_roles`) should have whoami files
generated, and which Netbox device status values (`device_statuses`) are
allowed. Both lists are matched case-insensitively.

The Netbox API token can also be passed in as an environment variable
`NETBOX_API_TOKEN`, which overrides the config file.

`request_timeout` is the per-request HTTP timeout in seconds (default 120).
`fetch_workers` is the number of concurrent Netbox API requests used to fetch
device pages (default 4).

Without `-config`, `machine_whoami` reads `machine_whoami.yaml` from the current
directory.

Example `/usr/local/etc/machine_whoami.yaml` file:

```yaml
---
output_dir: "/usr/local/configmaster/machine_whoami"
domain: example.com
netbox_url: https://netbox.example.com
netbox_token: 0123456789abcdef0123456789abcdef01234567
device_roles:
- router
- server
- switch
- workstation
device_statuses:
- active
- offline
- staged
- failed
- decommissioning
request_timeout: 120
fetch_workers: 4
```

### Running machine_whoami

After getting a Netbox API token and creating a configuration file, you can run
`machine_whoami` at any time by hand. By default it will generate whoami files for
all Netbox devices matching `device_roles` and `device_statuses` to the
specified output directory in the form `{output_dir}/{datacenter}/{fqdn}.txt`.
The packages also install a systemd timer that runs it every 10 minutes.

If you want to only generate whoami files for a certain Netbox region, e.g. to
reduce load, then region(s) can be specified as a comma separated list to the
`-regions` argument. E.g. `machine_whoami -regions east,metro_east`. Regions
match on either the Netbox region slug or name, case-insensitively. Cleanup of
stale files only touches datacenters that had devices processed in that run, so
region-filtered runs don't delete other regions' files.

If any device fails to write, the errors are listed in
`{output_dir}/failures.log`; the file is removed after a run with no failures.

### Serving machine_whoami output

Serving up the whoami files to clients is beyond the scope of this program.
Suggestions are to have Apache or Nginx serve up the output directory directly
(there are no secrets inside).

Then in the case of configuration management agents running on host, on each run
perform a conditional GET (If-Modified-Since) to fetch that individual server's
whoami .txt file. This is to ensure the whoami file is copied to the system if it
doesn't exist and only fetches it when it's changed.

For example in Chef this can be an Ohai plugin that checks file freshness from
the central Apache/Nginx server and also makes the whoami data available during
the Chef run as attributes.

### Example output

Here is an example of processing a region containing 9,000+ servers:

```bash
# /usr/local/bin/machine_whoami -config /usr/local/etc/machine_whoami.yaml -regions california
2026/10/27 13:30:56 Filtering for regions: [california]
2026/10/27 13:30:56 Fetched 6 sites
2026/10/27 13:30:58 Fetched 476 racks
2026/10/27 13:31:04 Fetching 10 device pages with 4 workers
2026/10/27 13:31:30 Fetched 9800 devices total
2026/10/27 13:31:30 Filtered to 9720 devices for regions: [california]
2026/10/27 13:31:30 Skipping device ID 14109: no name set
2026/10/27 13:31:30 Skipping device ID 14110: no name set
2026/10/27 13:31:30 Processing 9685 devices (fetched 9720, skipped: 13 no name, 22 wrong role, 0 wrong status)
2026/10/27 13:31:30 Summary: 9685 succeeded (9650 written, 35 unchanged), 0 failed out of 9685 processed
2026/10/27 13:31:30 machine_whoami generation complete in 34.4 seconds
```

## Packaging

The repo contains spec files and control information for building RPMs and DEB packages.
Also included are systemd unit files for the service and a timer to run `machine_whoami`
every 10 minutes with a 9 minute splay.

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE).
