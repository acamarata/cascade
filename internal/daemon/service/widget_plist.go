package service

// Purpose: render and read the widget LaunchAgent plist. The renderer emits
//   keys in the order `plutil -convert xml1` does (alphabetical) and escapes
//   every dynamic value. Status never reads a plist back: it compares the
//   file with a fresh render (see matchWidgetPlist).
// Inputs: the bundle executable, the environment map and the log path.
// Outputs: plist bytes.
// Constraints: pure functions, no filesystem or exec. The managed marker is
//   the daemon plist's, so writeManagedFile's clobber refusal covers both.
// SPORT: internal/daemon/service (P1-WID-04).

import (
	"bytes"
	"encoding/xml"
	"sort"
)

// widgetManagedMarker flags a plist cascade itself wrote.
const widgetManagedMarker = "<key>X-Cascade-Managed</key>"

// renderWidgetPlist builds the widget LaunchAgent plist.
func renderWidgetPlist(execPath string, env map[string]string, logPath string) []byte {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString("\t\t<key>" + escapeWidgetXML(k) + "</key>\n\t\t<string>" + escapeWidgetXML(env[k]) + "</string>\n")
	}
	b.WriteString("\t</dict>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<false/>\n")
	b.WriteString("\t<key>Label</key>\n\t<string>" + escapeWidgetXML(WidgetLabel) + "</string>\n")
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n\t\t<string>" + escapeWidgetXML(execPath) + "</string>\n\t</array>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>StandardErrorPath</key>\n\t<string>" + escapeWidgetXML(logPath) + "</string>\n")
	b.WriteString("\t<key>StandardOutPath</key>\n\t<string>" + escapeWidgetXML(logPath) + "</string>\n")
	b.WriteString("\t" + widgetManagedMarker + "\n\t<true/>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}

// isManagedWidgetPlist reports whether content carries the managed marker.
func isManagedWidgetPlist(content []byte) bool { return containsMarker(content, widgetManagedMarker) }

// escapeWidgetXML escapes s as XML character data.
func escapeWidgetXML(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}
