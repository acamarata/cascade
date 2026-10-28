// Package uninstall is `cascade uninstall` as typed data: Inventory lists
// every Cascade-owned install item (units, plugins, indexed vault entries,
// receipt-listed executables, the receipt, and the data-class elevation
// key, config and data directory), and Run removes them one by one through
// each item's own Remove, never touching a backup path.
//
// SPORT: internal/uninstall/ADDED.
package uninstall
