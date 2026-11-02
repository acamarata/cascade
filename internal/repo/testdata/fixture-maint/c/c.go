package c

import "fixturemaint/a"

// Gamma calls into package a, giving the graph an imports edge.
func Gamma() string { return a.Alpha() }
