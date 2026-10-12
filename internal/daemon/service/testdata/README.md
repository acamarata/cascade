# internal/daemon/service/testdata

Art.2 (12-QUALITY-CONSTITUTION.md, "real-counterpart") provenance for
`golden_launchd.plist` and `golden_systemd.service`, per Art.2.2 and this
ticket's own instruction ("provenance is stamped in
internal/daemon/service/testdata/README.md").

## golden_launchd.plist

- **Tool:** `plutil` (Apple's property-list utility), invoked as
  `plutil -convert xml1 -o golden_launchd.plist <source>.plist`.
- **Version / platform:** macOS 26.6.2 (BuildVersion 25G83), the `plutil`
  shipped at `/usr/bin/plutil` on the machine this ticket was built on.
- **Date:** 2026-09-03.
- **Method:** the required-key set (Label, ProgramArguments, KeepAlive,
  RunAtLoad, StandardOutPath, StandardErrorPath) and their exact
  alphabetical key ordering were taken from a REAL, already-installed
  macOS launchd user agent on this machine —
  `~/Library/LaunchAgents/com.ali.caffeinate-always.plist`, converted with
  `plutil -convert xml1` and inspected directly (KeepAlive/RunAtLoad/
  Label/ProgramArguments/StandardOutPath/StandardErrorPath, in that exact
  alphabetical order, is `plutil`'s real emitted order — dict keys are not
  hand-sorted here). A second source plist was then authored with
  cascade's own values (Label `com.acamarata.cascade`, a representative
  `/usr/local/bin/cascade daemon run` ProgramArguments array, and a
  representative log path) and passed through the SAME real `plutil
  -convert xml1` round-trip to produce `golden_launchd.plist` — so the
  file checked in is real plutil output, not hand-typed XML, and
  `plutil -lint` confirms it parses as a valid property list.
  `service_darwin_test.go` parses this golden with `howett.net/plist` (or
  the stdlib `encoding/xml` structural walk — see that file for which),
  extracts its key set, and asserts every one of those keys is also
  present in `renderLaunchdPlist`'s output — never a string/byte compare
  against the golden blob alone.
- **What is not exercised:** the golden does not include cascade's own
  `X-Cascade-Managed` marker key (an implementation detail of this
  package's clobber-refusal logic, not part of the Art.2 required-key
  contract) — `service_darwin_test.go` checks for that key separately,
  by name, not via the golden.

## golden_systemd.service

- **Tool:** `apt-get install syncthing` inside a `debian:12-slim` Docker
  container (this development machine is macOS and has no systemd
  instance to query directly; Docker was used to reach a real Debian
  package database rather than hand-authoring a unit from the manual
  page).
- **Version:** Debian 12 ("bookworm") `syncthing` package as resolved by
  `apt-get` on 2026-09-03; the real shipped unit is
  `/usr/lib/systemd/user/syncthing.service`.
- **Date:** 2026-09-03.
- **Method:** the real installed unit's [Unit]/[Service]/[Install]
  section shape and its `ExecStart=`/`Restart=on-failure`/`RestartSec=`/
  `WantedBy=default.target` keys were read directly from that file:
  ```
  [Unit]
  Description=Syncthing - Open Source Continuous File Synchronization
  Documentation=man:syncthing(1)
  StartLimitIntervalSec=60
  StartLimitBurst=4

  [Service]
  ExecStart=/usr/bin/syncthing serve --no-browser --no-restart --logflags=0
  Restart=on-failure
  RestartSec=1
  SuccessExitStatus=3 4
  RestartForceExitStatus=3 4
  SystemCallArchitectures=native
  MemoryDenyWriteExecute=true
  NoNewPrivileges=true

  [Install]
  WantedBy=default.target
  ```
  `golden_systemd.service` keeps this real unit's section layout and its
  `ExecStart=`/`Restart=on-failure`/`RestartSec=`/`WantedBy=default.target`
  keys, replacing the syncthing-specific `ExecStart=` value with cascade's
  own and dropping the hardening/documentation keys that are not part of
  this ticket's required set. **Deviation from the captured real unit:**
  the real syncthing unit relies on `Type=simple` being systemd's default
  (it does not state `Type=` explicitly) and has no `After=` line; this
  ticket's contract explicitly requires both `Type=simple` and
  `After=default.target` to be present as literal keys, so
  `golden_systemd.service` adds them — a documented addition, not a
  fabrication of section/key shape, which came entirely from the real
  captured file. `service_linux_test.go` parses this golden by section
  (`[Unit]`/`[Service]`/`[Install]`) and required key names and asserts
  every one of them is also present in `renderSystemdUnit`'s output —
  never a string/byte compare against the golden blob alone.

## launchctl_print_widget.txt

- **Command:** `launchctl print gui/$(id -u)/com.apple.quicklook`, stdout
  of a real loaded user agent (state `not running`, exit 0).
- **Platform:** macOS 27.0.1 (BuildVersion 26A434), captured 2026-10-12.
- **Substituted:** the label `com.apple.quicklook` became
  `com.acamarata.cascade.widget` (including the endpoint names); the plist
  path, `program` and `arguments` became synthetic `/Users/example/...`
  paths ending in `CascadeMenuBar.app/Contents/MacOS/CascadeMenuBar`; the
  working directory became `/Users/example`; the inherited
  `SSH_AUTH_SOCK` path became `/private/tmp/example.launchd/Listeners`; the
  system-service property was dropped. Structure, indentation and key order
  are the captured ones.
- **Not loaded:** a label launchd does not know exits 113 with
  `Could not find service ...` on stderr and nothing on stdout; the tests
  feed that shape to the parser for the not-loaded case.
- **Used by:** `widget_status_test.go`, `widget_darwin_status_test.go`.

## golden_launchd_widget.plist

- **Tool:** `plutil -convert xml1` (`/usr/bin/plutil`), macOS 27.0.1
  (BuildVersion 26A434), captured 2026-10-12.
- **Source:** the plist of a real widget agent loaded in the GUI domain
  (`launchctl print` exit 0, state running) by `cascade widget install`
  with a fresh HOME and CASCADE_HOME.
- **Normalised:** the fresh HOME became `/Users/example`, CASCADE_HOME
  became `/Users/example/.cascade`, the cascade binary became
  `/Users/example/bin/cascade-real`, the `CASCADE_HOME` entry (present
  only because that run set it) and the `X-Cascade-Managed` marker (a
  cascade-internal key, as for `golden_launchd.plist`) were removed. The
  result is byte-identical to `renderWidgetPlist`'s normalised output.
- **Used by:** `TestLaunchAgentUsesAbsoluteBundlePath`.
