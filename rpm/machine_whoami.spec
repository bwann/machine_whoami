Name:           machine-whoami
Version:        1.4
Release:        1%{?dist}
Summary:        Generate per-machine identity files from Netbox
License:        Apache-2.0
URL:            https://github.com/bwann/machine_whoami

Source0:        machine_whoami
Source1:        machine_whoami.yaml
Source2:        machine_whoami.service
Source3:        machine_whoami.timer

BuildArch:      x86_64
Requires:       systemd

%description
machine_whoami fetches device metadata from Netbox and writes per-machine
identity files in bash-compatible NAME=VALUE format. Designed for fleets
up to 100,000 devices across many sites/regions.

%install
install -D -m 0755 %{SOURCE0} %{buildroot}/usr/local/bin/machine_whoami
install -D -m 0640 %{SOURCE1} %{buildroot}/usr/local/etc/machine_whoami.yaml
install -D -m 0644 %{SOURCE2} %{buildroot}/etc/systemd/system/machine_whoami.service
install -D -m 0644 %{SOURCE3} %{buildroot}/etc/systemd/system/machine_whoami.timer
install -d -m 0755 %{buildroot}/var/tmp/machine_whoami

%files
%attr(0755, root, root) /usr/local/bin/machine_whoami
%config(noreplace) %attr(0640, root, root) /usr/local/etc/machine_whoami.yaml
/etc/systemd/system/machine_whoami.service
/etc/systemd/system/machine_whoami.timer
%dir /var/tmp/machine_whoami

%post
%systemd_post machine_whoami.timer

%preun
%systemd_preun machine_whoami.timer
%systemd_preun machine_whoami.service

%postun
%systemd_postun_with_restart machine_whoami.timer

%changelog
* Tue Oct 27 2026 Bryan Wann <bwann@wann.net> 1.4-1
- Replace DEVICE_SUBNET with DEVICE_SUBNET4 and DEVICE_SUBNET6, derived from
  the device's primary IPv4 and IPv6 addresses respectively; each is empty if
  unset. Scripts reading DEVICE_SUBNET must be updated. The format change
  rewrites every device file once.
- License under Apache-2.0
- Documentation updates; test data uses example.com and documentation
  address ranges

* Tue Oct 06 2026 Bryan Wann <bwann@wann.net> 1.3-1
- Write device files only when their Netbox data changes; unchanged files are
  left untouched (same inode, mtime, and content) for the device's lifetime
- Add DEVICE_WHOAMI_GENERATED_AT=<epoch> to output, recording when the file's
  content last changed; files from older versions are rewritten once to add it
- Run summary reports written vs. unchanged counts

* Sat Sep 19 2026 Bryan Wann <bwann@wann.net> 1.2-2
- Timer: run on OnCalendar=*:0/10 with RandomizedDelaySec=9min and FixedRandomDelay=true
  so each host keeps a stable, distinct offset and hostmaster systems do not hit Netbox at once
- Timer: AccuracySec=1s so the jitter is the only spread

* Mon Mar 16 2026 Bryan Wann <bwann@wann.net> 1.2-1
- Install systemd units to /etc/systemd/system/ instead of /usr/lib/systemd/system/
- Remove ProtectSystem and other sandboxing directives that blocked output directory writes
- Rename RPM from machine_whoami to machine-whoami

* Mon Mar 16 2026 Bryan Wann <bwann@wann.net> 1.0-1
- Initial package
